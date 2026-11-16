// Package broker is lodge's embedded event broker (ADR-0022;
// docs/specs/lodge-http.md, The broker; docs/specs/app-contract.md,
// Events). It runs a NATS server with JetStream inside lodge's process,
// with no listener of its own.
//
// It has 2 doors. An app daemon connects on /run/lodge/nats.sock, admitted
// by its user's uid (SO_PEERCRED). An outside client, never a browser,
// connects on /nats, the WebSocket route of lodge's lodge node. On either
// door lodge reads the connection's CONNECT line, decides who it is, and
// rewrites the line with a one-time secret in place of every credential, so
// the server admits exactly the identity lodge chose.
//
// Every app and every client is its own account: a reply subject can then
// only reach the account that sent the request, which a permission alone
// cannot stop (ADR-0022, the reply-subject finding).
package broker

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/frostyard/lodge/internal/apps"
)

// The server's options, and the limits of its doors.
const (
	// MaxPayload is the largest message the broker takes.
	MaxPayload = 64 << 10
	// MaxConn is the server's connection cap.
	MaxConn = 1024
	// MaxConnsPerIdentity caps the connections one app or one client holds.
	MaxConnsPerIdentity = 64
	// MaxConnectLine caps a connection's first line, its CONNECT.
	MaxConnectLine = 8 << 10
	// RefusalLogEvery is how often a refused uid is logged at most.
	RefusalLogEvery = time.Minute
)

// An app's stream: 1 per app, empty `>`.
const (
	// StreamMaxAge is how long a stream keeps a message.
	StreamMaxAge = 7 * 24 * time.Hour
	// StreamMaxBytes is how large a stream grows before it drops the
	// oldest messages.
	StreamMaxBytes = 1 << 30
	// DuplicateWindow is how long a publish's message ID is remembered.
	DuplicateWindow = 2 * time.Minute
)

// A consumer, which a pull never outlasts by more than a minute: a service
// export's response threshold is 2 minutes.
const (
	// PullExpires is the longest a pull request waits.
	PullExpires = 60 * time.Second
	// PullBatch is the largest batch one pull request may ask for.
	PullBatch = 256
)

// Lodge's own subjects: the reserved app id their first token is, and the
// resync event that a consumer gets when retention dropped messages it never
// saw (docs/specs/lodge-http.md, The broker).
const (
	// ReservedApp is the app id of lodge's own subjects. No app takes it, so
	// no app's subjects overlap them.
	ReservedApp = "lodge"
	// ResyncType is the CloudEvents type of the resync event.
	ResyncType = "org.frostyard.lodge.resync"
	// MsgIDHeader names a message's ID, which the stream deduplicates by.
	MsgIDHeader = "Nats-Msg-Id"
)

// ResyncSubject is the subject of the resync event of 1 consumer: lodge's
// own, which only lodge publishes.
func ResyncSubject(app, consumer string) string {
	return ReservedApp + "." + app + ".resync." + consumer
}

// resyncWildcard is every resync subject of 1 app, which its stream keeps.
func resyncWildcard(app string) string { return ReservedApp + "." + app + ".>" }

// The hub's subjects that a client's account reaches: its own pull
// requests, and its own acks.
const (
	nextPrefix = "$JS.API.CONSUMER.MSG.NEXT."
	ackPrefix  = "$JS.ACK."
	nextExport = nextPrefix + ">"
	ackExport  = ackPrefix + ">"
)

// The consumer metadata that brings the index back after a restart.
const (
	MetadataClient = "lodge.client"
	MetadataSub    = "lodge.sub"
	MetadataTopic  = "lodge.topic"
)

// The errors of a subscription that the caller answers apart.
var (
	// ErrConflict is a subscription whose app another subscription of the
	// client holds, or a client whose revoke is in progress.
	ErrConflict = errors.New("the subscription conflicts with another operation")
	// ErrNotFound is a subscription that is not there.
	ErrNotFound = errors.New("no such subscription")
	// ErrNoFilters is a subscription whose filters are empty: it would take
	// every subject.
	ErrNoFilters = errors.New("a subscription needs at least one filter")
)

// readyTimeout bounds the wait for the server's readiness.
const readyTimeout = 10 * time.Second

// Apps is what the broker needs of lodge's app registry.
type Apps interface {
	// AppForUID is the enabled app whose manifest's user has uid.
	AppForUID(uid int) (string, bool)
	// AppEnabled reports whether id is an enabled app.
	AppEnabled(id string) bool
}

// Options configure the broker.
type Options struct {
	// StoreDir is where JetStream keeps its store. The server makes its
	// own jetstream/ directory under it, so lodge's store is
	// /var/lib/lodge/jetstream.
	StoreDir string
	// Apps names an app socket's peer, and lodge's enabled apps.
	Apps Apps
	// DevUID, when not negative, is the developer's uid under --root: its
	// connections name their app in CONNECT's user field, and it must be
	// an enabled app.
	DevUID int
	// Tokens names the client a connection's auth_token belongs to, and when
	// that token expires: a zero time never expires. A nil Tokens refuses
	// every outside client.
	Tokens func(ctx context.Context, token string) (client string, expires time.Time, ok bool)
	// StreamMaxAge is how long a stream keeps a message [StreamMaxAge]. It
	// is a field so that a test can lower it: the server refuses a max age
	// under 100 ms.
	StreamMaxAge time.Duration
	// StreamDuplicates is how long a stream remembers a message ID
	// [DuplicateWindow]. The server refuses a window larger than the
	// stream's max age, and under 100 ms.
	StreamDuplicates time.Duration
	// Log is where the server's log goes [discard].
	Log *slog.Logger
}

// kind is what a connection is.
type kind int

const (
	kindHub kind = iota
	kindApp
	kindClient
)

// identity is whose connection it is.
type identity struct {
	kind kind
	// name is the app's id, the client's name, or "lodge".
	name string
	// token is the client's token, which CheckClients checks again.
	token string
}

// key is the identity's key in the connection registry.
func (i identity) key() string {
	switch i.kind {
	case kindApp:
		return "app:" + i.name
	case kindClient:
		return "client:" + i.name
	}
	return "hub"
}

// consumerRef is where a subscription's messages come from.
type consumerRef struct {
	stream   string
	consumer string
}

// clientState is one client's subscriptions.
type clientState struct {
	subs map[string]*consumerRef
	// topics are the topic each subscription asked for.
	topics map[string]string
}

// newClientState is a client with no subscription.
func newClientState() *clientState {
	return &clientState{subs: map[string]*consumerRef{}, topics: map[string]string{}}
}

// Broker is lodge's embedded broker.
type Broker struct {
	o   Options
	log *slog.Logger

	srv *server.Server
	hub *server.Account
	nc  *nats.Conn
	js  jetstream.JetStream

	// streamMaxAge and streamDuplicates are the limits of every app's
	// stream, which a test lowers.
	streamMaxAge     time.Duration
	streamDuplicates time.Duration

	// resyncMu makes each consumer's resync check and its publish 1 step, so
	// that 2 checks cannot both find no resync event pending and publish 2.
	// It is never held with b.mu.
	resyncMu sync.Mutex

	// peerUID reads a connection's peer uid (SO_PEERCRED); a test
	// replaces it.
	peerUID func(net.Conn) int

	mu sync.Mutex
	// pending are the one-time secrets the doors hand out, and the
	// identity each one names.
	pending map[string]identity
	// apps are the app ids that are ready: an account and a stream.
	apps map[string]bool
	// clients are the known clients, and their subscriptions.
	clients map[string]*clientState
	// revoking are the consumers a failed revoke kept for a client, which
	// its next revoke, or a subscription for it, deletes. Check never
	// reads it: a revoked client is out of clients.
	revoking map[string]map[consumerRef]bool
	// lifecycles make a client's subscribe and unsubscribe run 1 at a time,
	// so that its subscription stays on 1 app.
	lifecycles map[string]*sync.Mutex
	// epochs count each client's revokes, so that a subscription a revoke
	// overtakes sees it and deletes the consumer it made. A revoke takes
	// no lifecycle lock, and never waits for a subscription under way.
	epochs map[string]uint64
	// inflight are the revokes of each client that are still deleting,
	// which a subscription of that client waits out: the revoke's own
	// deletion would take the consumer it makes.
	inflight map[string]int
	// beforeRevokeDelete, when set, is a test's barrier: it runs after a
	// revoke takes the client, and before it deletes the consumers.
	beforeRevokeDelete func()
	// afterOneAppCheck, when set, is a test's barrier: it runs once a
	// subscription's one-app check has passed, and before its consumer is
	// made.
	afterOneAppCheck func()
	// accounts, exports and imports are what is registered, so that
	// nothing is registered twice.
	accounts map[string]*server.Account
	exports  map[string]map[string]bool
	imports  map[string]map[string]bool
	// conns are the connections each identity holds open.
	conns map[string]map[*conn]struct{}
	// refused is when each refused uid was last logged.
	refused map[int]time.Time
	closed  bool
	now     func() time.Time
}

// New returns a broker over o. It starts nothing until Start.
func New(o Options) *Broker {
	log := o.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Broker{
		o:                o,
		log:              log,
		streamMaxAge:     cmp.Or(o.StreamMaxAge, StreamMaxAge),
		streamDuplicates: cmp.Or(o.StreamDuplicates, DuplicateWindow),
		peerUID:          peerUID,
		pending:          map[string]identity{},
		apps:             map[string]bool{},
		clients:          map[string]*clientState{},
		revoking:         map[string]map[consumerRef]bool{},
		lifecycles:       map[string]*sync.Mutex{},
		epochs:           map[string]uint64{},
		inflight:         map[string]int{},
		accounts:         map[string]*server.Account{},
		exports:          map[string]map[string]bool{},
		imports:          map[string]map[string]bool{},
		conns:            map[string]map[*conn]struct{}{},
		refused:          map[int]time.Time{},
		now:              time.Now,
	}
}

// Start starts the server, its JetStream, and lodge's own client of it, and
// rebuilds the index of every client's consumers from the store.
func (b *Broker) Start(ctx context.Context) error {
	b.mu.Lock()
	if b.srv != nil {
		b.mu.Unlock()
		return errors.New("the broker is already started")
	}
	srv, err := server.NewServer(&server.Options{
		ServerName: "lodge",
		// Lodge has no listener for the broker: the 2 doors are its
		// listeners.
		DontListen: true,
		JetStream:  true,
		StoreDir:   b.o.StoreDir,
		// lodge owns SIGHUP and SIGTERM.
		NoSigs:                     true,
		MaxPayload:                 MaxPayload,
		MaxConn:                    MaxConn,
		CustomClientAuthentication: b,
	})
	if err != nil {
		b.mu.Unlock()
		return fmt.Errorf("start the broker: %w", err)
	}
	// The server's log is lodge's, at debug.
	srv.SetLoggerV2(slogLogger{b.log}, true, false, false)
	b.srv, b.hub = srv, srv.GlobalAccount()
	b.mu.Unlock()

	srv.Start()
	if !srv.ReadyForConnections(readyTimeout) {
		srv.Shutdown()
		return errors.New("start the broker: the server is not ready")
	}
	for _, e := range []struct {
		subject string
		resp    server.ServiceRespType
	}{{nextExport, server.Streamed}, {ackExport, server.Singleton}} {
		if err := b.export(b.hub, e.subject, e.resp); err != nil {
			b.Close()
			return err
		}
	}
	if err := b.connect(); err != nil {
		b.Close()
		return err
	}
	if err := b.rebuild(ctx); err != nil {
		b.Close()
		return err
	}
	return nil
}

// connect makes lodge's own client of the server, in process, in the hub
// account, with no permissions. It manages the streams and the consumers.
func (b *Broker) connect() error {
	secret, err := b.newSecret(identity{kind: kindHub, name: "lodge"})
	if err != nil {
		return err
	}
	nc, err := nats.Connect("", nats.InProcessServer(b.srv), nats.Token(secret), nats.NoReconnect(),
		nats.Name("lodge-broker"), nats.Timeout(readyTimeout),
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
			b.log.Debug("lodge's broker client", "err", err)
		}))
	if err != nil {
		return fmt.Errorf("connect lodge to the broker: %w", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return fmt.Errorf("connect lodge to JetStream: %w", err)
	}
	b.mu.Lock()
	b.nc, b.js = nc, js
	b.mu.Unlock()
	return nil
}

// Close closes every connection, lodge's own, and the server.
func (b *Broker) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	var conns []*conn
	for _, set := range b.conns {
		for c := range set {
			conns = append(conns, c)
		}
	}
	b.conns = map[string]map[*conn]struct{}{}
	nc, srv := b.nc, b.srv
	b.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
	if nc != nil {
		_ = nc.Drain()
	}
	if srv != nil {
		srv.Shutdown()
	}
}

// SyncApps creates the account and the stream of each app id, and closes
// the connections of an app that is no longer in the list. Its state stays:
// only the app's own removal leaves messages behind.
func (b *Broker) SyncApps(ctx context.Context, ids []string) error {
	b.mu.Lock()
	keep := make(map[string]bool, len(ids))
	for _, id := range ids {
		keep[id] = true
	}
	var gone []string
	for id := range b.apps {
		if !keep[id] {
			gone = append(gone, id)
		}
	}
	for _, id := range gone {
		delete(b.apps, id)
	}
	b.mu.Unlock()
	for _, id := range gone {
		b.closeConns(identity{kind: kindApp, name: id}.key())
	}
	var errs []error
	for _, id := range ids {
		if !apps.ValidID(id) {
			errs = append(errs, fmt.Errorf("sync the broker's apps: %q is not an app's id", id))
			continue
		}
		if err := b.ensureApp(ctx, id); err != nil {
			errs = append(errs, err)
			continue
		}
		b.mu.Lock()
		b.apps[id] = true
		b.mu.Unlock()
	}
	return errors.Join(errs...)
}

// ensureApp makes one app's account, its import of its own subject, and its
// stream.
func (b *Broker) ensureApp(ctx context.Context, id string) error {
	acc, err := b.account("app_" + id)
	if err != nil {
		return err
	}
	if err := b.exportAcc(b.hub, id+".>", acc, server.Singleton); err != nil {
		return err
	}
	if err := b.importAcc(acc, id+".>", id+".>"); err != nil {
		return err
	}
	if _, err := b.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: "app_" + id,
		// The app's own subjects, and lodge's own: its resync events,
		// which no app publishes.
		Subjects:   []string{id + ".>", resyncWildcard(id)},
		MaxAge:     b.streamMaxAge,
		MaxBytes:   StreamMaxBytes,
		Discard:    jetstream.DiscardOld,
		Storage:    jetstream.FileStorage,
		Duplicates: b.streamDuplicates,
	}); err != nil {
		return fmt.Errorf("make app %s's stream: %w", id, err)
	}
	return nil
}

// Spec is 1 subscription that a client asks for: its name, the app whose
// events it takes, and the topic it wants.
type Spec struct {
	Client string
	Sub    string
	App    string
	// Topic is the requested prefix of the subjects it takes, whose first
	// token is App.
	Topic string
	// Inactive makes a new subscription's consumer without the callback's
	// filters: it takes its resync subject alone, and delivers nothing until
	// the caller activates it with Refilter. A caller that reads a grant
	// outside the broker's lock uses it, so that the subscription is
	// registered before its filters are read: a grant change that runs after
	// that read then finds it. It applies to a new subscription only: 1 that
	// is registered already keeps the callback's filters, because its
	// consumer may hold events that its grant allows it to read.
	Inactive bool
}

// Subscribe gives client's subscription sub the messages of app that its
// filters take: 1 durable pull consumer, and one-time permissions that name
// it. The client's connections close, so that it reconnects with them. It is
// SubscribeWith with the app as the topic.
func (b *Broker) Subscribe(ctx context.Context, client, sub, app string, filters []string) error {
	_, err := b.SubscribeWith(ctx, Spec{Client: client, Sub: sub, App: app, Topic: app},
		func(context.Context) ([]string, error) { return filters, nil })
	return err
}

// SubscribeWith gives client's subscription sub the messages of spec's app
// that its filters take: 1 durable pull consumer, and one-time permissions
// that name it. filters runs inside the client's lifecycle lock, after the
// one-app check and before any consumer is made: its error, or an empty
// answer, stops the call there, and so does a filter list that the broker
// cannot install. It answers the consumer's filters: those the callback gave,
// and the subscription's own resync subject, or that subject alone for an
// inactive subscription. The client's connections close, so that it
// reconnects with them.
func (b *Broker) SubscribeWith(ctx context.Context, spec Spec, filters func(ctx context.Context) ([]string, error)) ([]string, error) {
	client, sub, app := spec.Client, spec.Sub, spec.App
	if err := validateName("client", client); err != nil {
		return nil, err
	}
	if err := validateName("sub", sub); err != nil {
		return nil, err
	}
	if !apps.ValidID(app) {
		return nil, fmt.Errorf("subscribe %s: %q is not an app's id", client, app)
	}
	if err := validateTopic(app, spec.Topic); err != nil {
		return nil, fmt.Errorf("subscribe %s to %s: %w", client, app, err)
	}
	// One operation of this client at a time: the check below and the
	// consumer it makes are 1 step.
	unlock := b.lifecycle(client)
	defer unlock()
	b.mu.Lock()
	epoch := b.epochs[client]
	revoking := b.inflight[client] > 0
	ready := b.apps[app]
	// A subscription stays on 1 app: its name is 1 consumer's, and a
	// second durable would leave the first one with nobody to read it.
	var on *consumerRef
	if c := b.clients[client]; c != nil && c.subs[sub] != nil {
		ref := *c.subs[sub]
		on = &ref
	}
	b.mu.Unlock()
	if revoking {
		// The revoke's own deletion would take the consumer this call
		// would make, so it waits for it to end, which takes 1 s at most.
		return nil, fmt.Errorf("subscribe %s: a revoke of the client is in progress: %w", client, ErrConflict)
	}
	if !ready {
		return nil, fmt.Errorf("subscribe %s to %s: the app is not enabled", client, app)
	}
	if on != nil && on.stream != "app_"+app {
		return nil, fmt.Errorf("%w: subscribe %s: subscription %q is %s's; unsubscribe it before subscribing it to %s",
			ErrConflict, client, sub, appOf(on.stream), app)
	}
	if hook := b.afterOneAppCheck; hook != nil {
		hook()
	}
	// The grant is read here, under the client's lock, so that the consumer
	// is made with the filters of the subscription's own grant.
	fs, err := filters(ctx)
	if err != nil {
		return nil, fmt.Errorf("subscribe %s to %s: %w", client, app, err)
	}
	if len(fs) == 0 {
		return nil, fmt.Errorf("subscribe %s to %s: the filters are empty: %w", client, app, ErrNoFilters)
	}
	if err := validateFilters(app, fs); err != nil {
		return nil, fmt.Errorf("subscribe %s to %s: %w", client, app, err)
	}
	// A consumer's filters are never empty, which would mean every subject:
	// its resync subject is always one of them, and the count includes it.
	resync := ResyncSubject(app, client+"-"+sub)
	fs = append(fs, resync)
	if err := validateCount(fs); err != nil {
		return nil, fmt.Errorf("subscribe %s to %s: %w", client, app, err)
	}
	if spec.Inactive && on == nil {
		// It is a new subscription: the callback's answer is the precheck,
		// and the filters go in when the caller activates it, which reads the
		// grant again. A subscription that is registered already keeps them
		// now: its consumer may hold events that its grant lets it read, and
		// a narrower filter in between would move it past them.
		fs = []string{resync}
	}
	// A revoke that could not delete a consumer kept it for this client,
	// and it stays first: a kept ref of this subscription's app would
	// delete the consumer this call is about to make.
	if err := b.retryRevoke(ctx, client); err != nil {
		return nil, err
	}
	acc, err := b.account("client_" + client)
	if err != nil {
		return nil, err
	}
	ref := consumerRef{stream: "app_" + app, consumer: client + "-" + sub}
	if err := b.addImports(acc, ref); err != nil {
		return nil, err
	}
	if _, err := b.js.CreateOrUpdateConsumer(ctx, ref.stream, jetstream.ConsumerConfig{
		Durable:           ref.consumer,
		AckPolicy:         jetstream.AckExplicitPolicy,
		DeliverPolicy:     jetstream.DeliverNewPolicy,
		FilterSubjects:    fs,
		MaxRequestExpires: PullExpires,
		MaxRequestBatch:   PullBatch,
		Metadata:          map[string]string{MetadataClient: client, MetadataSub: sub, MetadataTopic: spec.Topic},
	}); err != nil {
		return nil, fmt.Errorf("subscribe %s to %s: %w", client, app, err)
	}
	// A revoke that ran meanwhile took the client and its consumers: this
	// one is nobody's, and it goes again.
	b.mu.Lock()
	moved := b.epochs[client] != epoch
	if !moved {
		c := b.clients[client]
		if c == nil {
			c = newClientState()
			b.clients[client] = c
		}
		c.subs[sub] = &ref
		c.topics[sub] = spec.Topic
	}
	b.mu.Unlock()
	if moved {
		dctx, cancel := context.WithTimeout(ctx, revokeTimeout)
		defer cancel()
		kept, err := b.deleteRefs(dctx, []consumerRef{ref})
		b.keepRefs(client, kept)
		if err != nil {
			return nil, fmt.Errorf("subscribe %s to %s: the client was revoked during the subscription, and its consumer is kept: %w", client, app, err)
		}
		return nil, fmt.Errorf("subscribe %s to %s: the client was revoked during the subscription", client, app)
	}
	b.closeConns(identity{kind: kindClient, name: client}.key())
	return fs, nil
}

// Unsubscribe deletes client's subscription sub and its consumer, and
// closes the client's connections.
func (b *Broker) Unsubscribe(ctx context.Context, client, sub string) error {
	if err := validateName("client", client); err != nil {
		return err
	}
	if err := validateName("sub", sub); err != nil {
		return err
	}
	unlock := b.lifecycle(client)
	defer unlock()
	return b.unsubscribeLocked(ctx, client, sub)
}

// unsubscribeLocked is Unsubscribe under the client's lifecycle lock, which
// the caller holds.
func (b *Broker) unsubscribeLocked(ctx context.Context, client, sub string) error {
	b.mu.Lock()
	epoch := b.epochs[client]
	c := b.clients[client]
	var ref *consumerRef
	if c != nil {
		ref = c.subs[sub]
	}
	b.mu.Unlock()
	if ref == nil {
		return fmt.Errorf("unsubscribe %s: no subscription %q: %w", client, sub, ErrNotFound)
	}
	if err := b.js.DeleteConsumer(ctx, ref.stream, ref.consumer); err != nil && !errors.Is(err, jetstream.ErrConsumerNotFound) {
		return fmt.Errorf("unsubscribe %s/%s: %w", client, sub, err)
	}
	// A revoke that ran meanwhile removed the client, so there is nothing
	// left to take out of the index.
	b.mu.Lock()
	if b.epochs[client] == epoch {
		if c := b.clients[client]; c != nil {
			delete(c.subs, sub)
			delete(c.topics, sub)
		}
	}
	b.mu.Unlock()
	b.closeConns(identity{kind: kindClient, name: client}.key())
	return nil
}

// revokeTimeout bounds the deletion of a revoked client's consumers, so a
// revoke finishes whether or not the server answers.
const revokeTimeout = time.Second

// Revoke forgets client, closes its connections at once, and deletes every
// consumer of its subscriptions, concurrently and within revokeTimeout. The
// caller revokes the client's token first: a connection that is admitted
// meanwhile then fails the door's second token check.
func (b *Broker) Revoke(ctx context.Context, client string) error {
	if err := validateName("client", client); err != nil {
		return err
	}
	// The whole revoke has 1 budget, and it waits for nothing: a
	// subscription that is under way sees the new epoch and deletes the
	// consumer it makes.
	ctx, cancel := context.WithTimeout(ctx, revokeTimeout)
	defer cancel()
	b.mu.Lock()
	b.epochs[client]++
	b.inflight[client]++
	defer b.endRevoke(client)
	refs := b.refsLocked(client)
	delete(b.clients, client)
	delete(b.revoking, client)
	b.mu.Unlock()
	b.closeConns(identity{kind: kindClient, name: client}.key())
	if hook := b.beforeRevokeDelete; hook != nil {
		hook()
	}
	if len(refs) == 0 {
		return nil
	}
	kept, err := b.deleteRefs(ctx, refs)
	b.keepRefs(client, kept)
	return err
}

// endRevoke records that a client's revoke has finished deleting.
func (b *Broker) endRevoke(client string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.inflight[client]--; b.inflight[client] <= 0 {
		delete(b.inflight, client)
	}
}

// lifecycle locks a client's subscription lifecycle, and answers what
// unlocks it: a subscribe, an unsubscribe and a revoke of 1 client run 1 at
// a time. It never holds b.mu.
func (b *Broker) lifecycle(client string) func() {
	b.mu.Lock()
	m := b.lifecycles[client]
	if m == nil {
		m = &sync.Mutex{}
		b.lifecycles[client] = m
	}
	b.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// refsLocked answers every consumer a client's revoke must delete: its
// subscriptions' and those a failed revoke kept. b.mu must be held.
func (b *Broker) refsLocked(client string) []consumerRef {
	seen := map[consumerRef]bool{}
	var refs []consumerRef
	add := func(ref consumerRef) {
		if !seen[ref] {
			seen[ref] = true
			refs = append(refs, ref)
		}
	}
	if c := b.clients[client]; c != nil {
		for _, ref := range c.subs {
			add(*ref)
		}
	}
	for ref := range b.revoking[client] {
		add(ref)
	}
	return refs
}

// deleteRefs deletes consumers concurrently, within the budget of the ctx it
// is given, and answers the ones that are still there.
func (b *Broker) deleteRefs(ctx context.Context, refs []consumerRef) ([]consumerRef, error) {
	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		kept []consumerRef
		errs []error
	)
	for _, ref := range refs {
		wg.Go(func() {
			err := b.js.DeleteConsumer(ctx, ref.stream, ref.consumer)
			if err == nil || errors.Is(err, jetstream.ErrConsumerNotFound) {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			kept = append(kept, ref)
			errs = append(errs, fmt.Errorf("delete the consumer %s of %s: %w", ref.consumer, ref.stream, err))
		})
	}
	wg.Wait()
	slices.SortFunc(kept, func(a, c consumerRef) int {
		return strings.Compare(a.stream+"\x00"+a.consumer, c.stream+"\x00"+c.consumer)
	})
	return kept, errors.Join(errs...)
}

// keepRefs remembers the consumers a revoke could not delete, so that its
// next try, or a subscription for the client, finds them.
func (b *Broker) keepRefs(client string, refs []consumerRef) {
	if len(refs) == 0 {
		return
	}
	b.mu.Lock()
	set := b.revoking[client]
	if set == nil {
		set = map[consumerRef]bool{}
		b.revoking[client] = set
	}
	for _, ref := range refs {
		set[ref] = true
	}
	b.mu.Unlock()
	b.log.Warn("the broker kept a revoked client's consumer to delete later", "client", client, "refs", len(refs))
}

// retryRevoke deletes the consumers a failed revoke kept for client, and
// keeps those it cannot delete yet. It answers an error while one stays, so
// that a subscription of that client creates nothing.
func (b *Broker) retryRevoke(ctx context.Context, client string) error {
	b.mu.Lock()
	refs := make([]consumerRef, 0, len(b.revoking[client]))
	for ref := range b.revoking[client] {
		refs = append(refs, ref)
	}
	delete(b.revoking, client)
	b.mu.Unlock()
	if len(refs) == 0 {
		return nil
	}
	dctx, cancel := context.WithTimeout(ctx, revokeTimeout)
	defer cancel()
	kept, err := b.deleteRefs(dctx, refs)
	b.keepRefs(client, kept)
	if err != nil {
		return fmt.Errorf("subscribe %s: a consumer its revoke kept is still there: %w", client, err)
	}
	return nil
}

// appOf is the app a stream belongs to: app_ID is ID's.
func appOf(stream string) string { return strings.TrimPrefix(stream, "app_") }

// Check admits one connection to the server. The CONNECT line's token is a
// one-time secret, which names the identity a door decided; the secret is
// used up here.
func (b *Broker) Check(c server.ClientAuthentication) bool {
	if c.Kind() != server.CLIENT {
		return false
	}
	var token string
	if opts := c.GetOpts(); opts != nil {
		token = opts.Token
	}
	user, ok := b.admit(token)
	if !ok {
		return false
	}
	c.RegisterUser(user)
	return true
}

// admit takes the secret's identity out of the index, and answers with the
// user it names. It reads only the index: a door has done every other check.
func (b *Broker) admit(token string) (*server.User, bool) {
	if token == "" {
		return nil, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	idn, ok := b.pending[token]
	if !ok {
		return nil, false
	}
	delete(b.pending, token)
	switch idn.kind {
	case kindHub:
		// The hub is the account the server made this connection in.
		return &server.User{Username: "lodge"}, true
	case kindApp:
		if !b.apps[idn.name] {
			return nil, false
		}
		acc := b.accounts["app_"+idn.name]
		if acc == nil {
			return nil, false
		}
		return &server.User{
			Username: "app:" + idn.name,
			Account:  acc,
			Permissions: &server.Permissions{
				Publish:   &server.SubjectPermission{Allow: []string{idn.name + ".>"}},
				Subscribe: &server.SubjectPermission{Allow: []string{"_INBOX.>"}},
			},
		}, true
	case kindClient:
		c := b.clients[idn.name]
		if c == nil {
			return nil, false
		}
		acc := b.accounts["client_"+idn.name]
		if acc == nil {
			return nil, false
		}
		return &server.User{Username: "client:" + idn.name, Account: acc, Permissions: clientPerms(idn.name, c)}, true
	}
	return nil, false
}

// clientPerms is a client's permissions: its own consumers' pull requests
// and acks, and nothing else. A nil Allow list would allow every subject, so
// a client with no consumer denies them all.
func clientPerms(name string, c *clientState) *server.Permissions {
	pub := &server.SubjectPermission{}
	for _, ref := range c.subs {
		pub.Allow = append(pub.Allow, nextPrefix+ref.stream+"."+ref.consumer, ackPrefix+ref.stream+"."+ref.consumer+".>")
	}
	slices.Sort(pub.Allow)
	if len(pub.Allow) == 0 {
		pub.Deny = []string{">"}
	}
	return &server.Permissions{
		Publish:   pub,
		Subscribe: &server.SubjectPermission{Allow: []string{"_INBOX." + name + ".>"}},
	}
}

// newSecret makes a one-time secret for idn.
func (b *Broker) newSecret(idn identity) (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("make a connection's secret: %w", err)
	}
	secret := hex.EncodeToString(raw[:])
	b.mu.Lock()
	b.pending[secret] = idn
	b.mu.Unlock()
	return secret, nil
}

// forget drops a secret that the server did not use.
func (b *Broker) forget(secret string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.pending, secret)
}

// rebuild reads every app stream's consumers back into the index, with the
// accounts and the imports they need.
func (b *Broker) rebuild(ctx context.Context) error {
	streams := b.js.ListStreams(ctx)
	for info := range streams.Info() {
		stream := info.Config.Name
		if !strings.HasPrefix(stream, "app_") {
			continue
		}
		st, err := b.js.Stream(ctx, stream)
		if err != nil {
			if errors.Is(err, jetstream.ErrStreamNotFound) {
				continue
			}
			return fmt.Errorf("read the stream %s: %w", stream, err)
		}
		consumers := st.ListConsumers(ctx)
		for ci := range consumers.Info() {
			if err := b.restore(stream, ci); err != nil {
				return err
			}
		}
		if err := consumers.Err(); err != nil {
			return fmt.Errorf("read %s's consumers: %w", stream, err)
		}
	}
	if err := streams.Err(); err != nil {
		return fmt.Errorf("read the broker's streams: %w", err)
	}
	return nil
}

// restore puts one stored consumer back in the index.
func (b *Broker) restore(stream string, info *jetstream.ConsumerInfo) error {
	client, sub := info.Config.Metadata[MetadataClient], info.Config.Metadata[MetadataSub]
	if client == "" || sub == "" {
		return nil
	}
	acc, err := b.account("client_" + client)
	if err != nil {
		return err
	}
	ref := consumerRef{stream: stream, consumer: info.Name}
	if err := b.addImports(acc, ref); err != nil {
		return err
	}
	b.mu.Lock()
	c := b.clients[client]
	if c == nil {
		c = newClientState()
		b.clients[client] = c
	}
	c.subs[sub] = &ref
	// A consumer that an older lodge made has no topic: it asked for its
	// whole app.
	topic := info.Config.Metadata[MetadataTopic]
	if topic == "" {
		topic = appOf(stream)
	}
	c.topics[sub] = topic
	b.mu.Unlock()
	return nil
}

// account is the account named name, made on first use.
func (b *Broker) account(name string) (*server.Account, error) {
	b.mu.Lock()
	acc, ok := b.accounts[name]
	srv := b.srv
	b.mu.Unlock()
	if ok {
		return acc, nil
	}
	made, err := srv.RegisterAccount(name)
	if errors.Is(err, server.ErrAccountExists) {
		// Another caller made it in between.
		made, err = srv.LookupAccount(name)
	}
	if err != nil {
		return nil, fmt.Errorf("make the account %s: %w", name, err)
	}
	b.mu.Lock()
	b.accounts[name] = made
	b.mu.Unlock()
	return made, nil
}

// export makes subject an export of hub, with response type resp, for acc
// alone when it is not nil.
func (b *Broker) export(hub *server.Account, subject string, resp server.ServiceRespType) error {
	return b.exportAcc(hub, subject, nil, resp)
}

// exportAcc makes subject an export of hub for acc alone; a nil acc is every
// account. It registers each export once.
func (b *Broker) exportAcc(hub *server.Account, subject string, acc *server.Account, resp server.ServiceRespType) error {
	b.mu.Lock()
	if b.exports[hub.Name][subject] {
		b.mu.Unlock()
		return nil
	}
	b.mu.Unlock()
	var accounts []*server.Account
	if acc != nil {
		accounts = []*server.Account{acc}
	}
	if err := hub.AddServiceExportWithResponse(subject, resp, accounts); err != nil {
		return fmt.Errorf("export %s: %w", subject, err)
	}
	b.mu.Lock()
	if b.exports[hub.Name] == nil {
		b.exports[hub.Name] = map[string]bool{}
	}
	b.exports[hub.Name][subject] = true
	b.mu.Unlock()
	return nil
}

// importAcc adds acc's import of from in hub, as to.
func (b *Broker) importAcc(acc *server.Account, from, to string) error {
	b.mu.Lock()
	hub := b.hub
	done := b.imports[acc.Name][from]
	b.mu.Unlock()
	if done {
		return nil
	}
	if err := acc.AddServiceImport(hub, from, to); err != nil {
		return fmt.Errorf("import %s: %w", from, err)
	}
	b.record(acc, from)
	return nil
}

// addImports gives acc the reach its consumer needs: its own pull requests,
// and its own acks.
func (b *Broker) addImports(acc *server.Account, ref consumerRef) error {
	for _, from := range []string{
		nextPrefix + ref.stream + "." + ref.consumer,
		ackPrefix + ref.stream + "." + ref.consumer + ".>",
	} {
		if err := b.importAcc(acc, from, from); err != nil {
			return err
		}
	}
	return nil
}

// record marks subject as imported by acc.
func (b *Broker) record(acc *server.Account, subject string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.imports[acc.Name] == nil {
		b.imports[acc.Name] = map[string]bool{}
	}
	b.imports[acc.Name][subject] = true
}

// peer is a connection's peer uid.
func (b *Broker) peer(c net.Conn) int {
	b.mu.Lock()
	f := b.peerUID
	b.mu.Unlock()
	return f(c)
}

// setPeerUID replaces the way a peer's uid is read, for a test.
func (b *Broker) setPeerUID(f func(net.Conn) int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.peerUID = f
}

// track registers c under idn, or refuses it past the identity's cap.
func (b *Broker) track(idn identity, c net.Conn) (*conn, bool) {
	tc := &conn{Conn: c, b: b, key: idn.key(), token: idn.token}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return tc, false
	}
	set := b.conns[tc.key]
	if set == nil {
		set = map[*conn]struct{}{}
		b.conns[tc.key] = set
	}
	if len(set) >= MaxConnsPerIdentity {
		return tc, false
	}
	set[tc] = struct{}{}
	return tc, true
}

// closeConns closes every connection an identity holds.
func (b *Broker) closeConns(key string) int {
	b.mu.Lock()
	set := b.conns[key]
	delete(b.conns, key)
	conns := make([]*conn, 0, len(set))
	for c := range set {
		conns = append(conns, c)
	}
	b.mu.Unlock()
	// Each close may wait on its transport, so they go together.
	var wg sync.WaitGroup
	for _, c := range conns {
		wg.Go(func() { _ = c.Close() })
	}
	wg.Wait()
	return len(conns)
}

// conn is a tracked connection.
type conn struct {
	net.Conn
	b   *Broker
	key string
	// token is the client's token, which CheckClients checks again.
	token string
	once  sync.Once
}

// Close closes the connection and forgets it.
func (c *conn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() {
		c.b.mu.Lock()
		defer c.b.mu.Unlock()
		if set := c.b.conns[c.key]; set != nil {
			delete(set, c)
			if len(set) == 0 {
				delete(c.b.conns, c.key)
			}
		}
	})
	return err
}

// logRefused logs a uid that is no enabled app's, at most once a minute.
func (b *Broker) logRefused(uid int) {
	b.mu.Lock()
	now := b.now()
	last, seen := b.refused[uid]
	write := !seen || now.Sub(last) >= RefusalLogEvery
	if write {
		b.refused[uid] = now
	}
	b.mu.Unlock()
	if write {
		b.log.Warn("the broker socket refused a peer that is not an enabled app's user", "uid", uid)
	}
}

// slogLogger is nats-server's log over slog.
type slogLogger struct{ log *slog.Logger }

func (l slogLogger) Noticef(format string, v ...any) { l.log.Debug(fmt.Sprintf(format, v...)) }
func (l slogLogger) Debugf(format string, v ...any)  { l.log.Debug(fmt.Sprintf(format, v...)) }
func (l slogLogger) Tracef(format string, v ...any)  { l.log.Debug(fmt.Sprintf(format, v...)) }
func (l slogLogger) Warnf(format string, v ...any)   { l.log.Warn(fmt.Sprintf(format, v...)) }
func (l slogLogger) Errorf(format string, v ...any)  { l.log.Error(fmt.Sprintf(format, v...)) }
func (l slogLogger) Fatalf(format string, v ...any)  { l.log.Error(fmt.Sprintf(format, v...)) }
