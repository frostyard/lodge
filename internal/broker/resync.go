package broker

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// resyncTimeout bounds the resync of 1 client, which runs while the client
// connects.
const resyncTimeout = 2 * time.Second

// Resync publishes 1 resync event for each of client's consumers that
// retention overtook: a consumer whose position is older than the first
// message its stream still holds never saw the messages that went, so it
// gets 1 event that says its state is stale (docs/specs/lodge-http.md, The
// broker).
//
// A resync event that is still pending for a consumer is not published
// again. The server moves a consumer's position only when it serves a pull,
// so a resync is published while the gap is still visible: on the client's
// connection, before the server serves it, and in the sweep.
func (b *Broker) Resync(ctx context.Context, client string) error {
	if err := validateName("client", client); err != nil {
		return err
	}
	b.mu.Lock()
	var refs []consumerRef
	if c := b.clients[client]; c != nil {
		for _, ref := range c.subs {
			refs = append(refs, *ref)
		}
	}
	b.mu.Unlock()
	if len(refs) == 0 {
		return nil
	}
	var errs []error
	for _, ref := range refs {
		msgs, err := b.resyncNeeded(ctx, ref)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !msgs {
			continue
		}
		if err := b.publishResync(ctx, ref); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ResyncAll runs Resync for every client.
func (b *Broker) ResyncAll(ctx context.Context) error {
	b.mu.Lock()
	clients := make([]string, 0, len(b.clients))
	for client := range b.clients {
		clients = append(clients, client)
	}
	b.mu.Unlock()
	slices.Sort(clients)
	var errs []error
	for _, client := range clients {
		if err := b.Resync(ctx, client); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// resyncNeeded reports whether ref's consumer missed messages that its
// stream dropped, and has no resync event pending.
func (b *Broker) resyncNeeded(ctx context.Context, ref consumerRef) (bool, error) {
	c, err := b.js.Consumer(ctx, ref.stream, ref.consumer)
	if errors.Is(err, jetstream.ErrConsumerNotFound) || errors.Is(err, jetstream.ErrStreamNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read the consumer %s of %s: %w", ref.consumer, ref.stream, err)
	}
	info, err := c.Info(ctx)
	if err != nil {
		return false, fmt.Errorf("read the consumer %s of %s: %w", ref.consumer, ref.stream, err)
	}
	st, err := b.js.Stream(ctx, ref.stream)
	if err != nil {
		return false, fmt.Errorf("read the stream %s: %w", ref.stream, err)
	}
	state, err := st.Info(ctx)
	if err != nil {
		return false, fmt.Errorf("read the stream %s: %w", ref.stream, err)
	}
	if info.Delivered.Stream+1 >= state.State.FirstSeq {
		return false, nil
	}
	last, err := st.GetLastMsgForSubject(ctx, ResyncSubject(appOf(ref.stream), ref.consumer))
	if err != nil && !errors.Is(err, jetstream.ErrMsgNotFound) {
		return false, fmt.Errorf("read the resync event of %s: %w", ref.consumer, err)
	}
	if err == nil && last.Sequence > info.Delivered.Stream {
		// A resync event this consumer has not read yet is pending.
		return false, nil
	}
	return true, nil
}

// publishResync publishes 1 resync event on ref's consumer's subject, in the
// hub account, and waits for JetStream's acknowledgement: the event is stored
// before the client that needed it reads on. The message ID is the event's
// own, so that the stream deduplicates a second publish of it.
func (b *Broker) publishResync(ctx context.Context, ref consumerRef) error {
	body, id, err := resyncEvent(appOf(ref.stream), b.now())
	if err != nil {
		return err
	}
	msg := nats.NewMsg(ResyncSubject(appOf(ref.stream), ref.consumer))
	msg.Data = body
	msg.Header.Set(MsgIDHeader, id)
	b.mu.Lock()
	js := b.js
	b.mu.Unlock()
	if js == nil {
		return errors.New("publish a resync event: the broker is not started")
	}
	if _, err := js.PublishMsg(ctx, msg); err != nil {
		return fmt.Errorf("publish a resync event for %s: %w", ref.consumer, err)
	}
	return nil
}

// resyncEnvelope is a resync event's CloudEvents 1.0 structured form.
type resyncEnvelope struct {
	SpecVersion string `json:"specversion"`
	ID          string `json:"id"`
	Source      string `json:"source"`
	Type        string `json:"type"`
	Subject     string `json:"subject"`
	Time        string `json:"time"`
}

// resyncEvent returns the body and the id of 1 app's resync event.
func resyncEvent(app string, now time.Time) ([]byte, string, error) {
	id, err := newID(now)
	if err != nil {
		return nil, "", fmt.Errorf("make a resync event's id: %w", err)
	}
	body, err := json.Marshal(resyncEnvelope{
		SpecVersion: "1.0",
		ID:          id,
		Source:      "/lodge",
		Type:        ResyncType,
		Subject:     app,
		Time:        now.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return nil, "", fmt.Errorf("make a resync event: %w", err)
	}
	return body, id, nil
}

// newID returns a UUIDv7 (RFC 9562) of 1 time: its millisecond count,
// version 7, variant 10, and 10 random bytes.
func newID(now time.Time) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[6:]); err != nil {
		return "", err
	}
	ms := uint64(now.UnixMilli())
	for i := range 6 {
		raw[i] = byte(ms >> (40 - 8*i))
	}
	raw[6] = raw[6]&0x0f | 0x70
	raw[8] = raw[8]&0x3f | 0x80
	encoded := hex.EncodeToString(raw[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32], nil
}

// CheckClients checks the token of each open client connection again, and
// closes the ones whose token is gone or now names another client: a token
// that lodge revoked stops working before the store's next sweep.
func (b *Broker) CheckClients(ctx context.Context) {
	tokens := b.o.Tokens
	if tokens == nil {
		return
	}
	b.mu.Lock()
	var conns []*conn
	for key, set := range b.conns {
		if !strings.HasPrefix(key, "client:") {
			continue
		}
		for c := range set {
			conns = append(conns, c)
		}
	}
	b.mu.Unlock()
	for _, c := range conns {
		name := strings.TrimPrefix(c.key, "client:")
		if named, _, ok := tokens(ctx, c.token); !ok || named != name {
			b.log.Info("the broker closed a connection whose token is gone", "client", name)
			_ = c.Close()
		}
	}
}

// Subscriptions answers every subscription of every client, sorted.
func (b *Broker) Subscriptions() []Subscription {
	b.mu.Lock()
	out := make([]Subscription, 0, len(b.clients))
	for client, c := range b.clients {
		for sub, ref := range c.subs {
			out = append(out, Subscription{Client: client, Sub: sub, App: appOf(ref.stream), Topic: c.topics[sub],
				Stream: ref.stream, Consumer: ref.consumer})
		}
	}
	b.mu.Unlock()
	return sortSubscriptions(out)
}

// ClientSubscriptions answers client's subscriptions, sorted.
func (b *Broker) ClientSubscriptions(client string) []Subscription {
	b.mu.Lock()
	var out []Subscription
	if c := b.clients[client]; c != nil {
		for sub, ref := range c.subs {
			out = append(out, Subscription{Client: client, Sub: sub, App: appOf(ref.stream), Topic: c.topics[sub],
				Stream: ref.stream, Consumer: ref.consumer})
		}
	}
	b.mu.Unlock()
	return sortSubscriptions(out)
}

// Subscription is 1 subscription of a client: its name, the app and the
// topic it takes, and the consumer that delivers it.
type Subscription struct {
	Client   string
	Sub      string
	App      string
	Topic    string
	Stream   string
	Consumer string
}

// sortSubscriptions sorts subscriptions by client and name.
func sortSubscriptions(subs []Subscription) []Subscription {
	out := subs
	if out == nil {
		out = []Subscription{}
	}
	slices.SortFunc(out, func(a, c Subscription) int {
		return cmp.Or(strings.Compare(a.Client, c.Client), strings.Compare(a.Sub, c.Sub))
	})
	return out
}

// StreamState is what 1 app's stream holds, and what it keeps.
type StreamState struct {
	// MaxAge is how long the stream keeps a message.
	MaxAge time.Duration
	// MaxBytes is how large the stream grows before it drops the oldest
	// messages.
	MaxBytes int64
	// DiscardOld reports whether the stream drops its oldest messages.
	DiscardOld bool
	// Subjects are the subjects the stream takes.
	Subjects []string
	// FirstSeq is the stream's first message's sequence, LastSeq its last,
	// and Msgs how many it holds.
	FirstSeq, LastSeq, Msgs uint64
}

// StreamState answers what app's stream holds and keeps, for a caller that
// checks the broker's limits.
func (b *Broker) StreamState(ctx context.Context, app string) (StreamState, error) {
	st, err := b.js.Stream(ctx, "app_"+app)
	if err != nil {
		return StreamState{}, fmt.Errorf("read the stream of %s: %w", app, err)
	}
	info, err := st.Info(ctx)
	if err != nil {
		return StreamState{}, fmt.Errorf("read the stream of %s: %w", app, err)
	}
	return StreamState{MaxAge: info.Config.MaxAge, MaxBytes: info.Config.MaxBytes,
		DiscardOld: info.Config.Discard == jetstream.DiscardOld, Subjects: info.Config.Subjects,
		FirstSeq: info.State.FirstSeq, LastSeq: info.State.LastSeq, Msgs: info.State.Msgs}, nil
}

// ConsumerState is what 1 consumer takes, and how far it has got.
type ConsumerState struct {
	// Filters are the consumer's filters.
	Filters []string
	// Delivered is the stream sequence of the last message the consumer
	// was given, and AckFloor the last it acknowledged.
	Delivered, AckFloor uint64
}

// ConsumerState answers what 1 consumer takes and how far it has got, for a
// caller that checks a subscription.
func (b *Broker) ConsumerState(ctx context.Context, stream, consumer string) (ConsumerState, error) {
	c, err := b.js.Consumer(ctx, stream, consumer)
	if errors.Is(err, jetstream.ErrConsumerNotFound) || errors.Is(err, jetstream.ErrStreamNotFound) {
		return ConsumerState{}, fmt.Errorf("read the consumer %s of %s: %w", consumer, stream, ErrNotFound)
	}
	if err != nil {
		return ConsumerState{}, fmt.Errorf("read the consumer %s of %s: %w", consumer, stream, err)
	}
	info, err := c.Info(ctx)
	if err != nil {
		return ConsumerState{}, fmt.Errorf("read the consumer %s of %s: %w", consumer, stream, err)
	}
	return ConsumerState{Filters: info.Config.FilterSubjects, Delivered: info.Delivered.Stream,
		AckFloor: info.AckFloor.Stream}, nil
}
