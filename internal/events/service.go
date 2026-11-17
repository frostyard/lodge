package events

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/frostyard/lodge/internal/apps"
	"github.com/frostyard/lodge/internal/broker"
	"github.com/frostyard/lodge/internal/oauth"
)

// SweepEvery is how often the service checks the subscriptions and the
// clients.
const SweepEvery = time.Minute

// opTimeout bounds 1 pass of the service over the broker and the OAuth
// server, so that a hook never waits on a store that does not answer.
const opTimeout = 5 * time.Second

// The errors of a subscription, which the gateway answers apart.
var (
	// ErrInvalid is a subscription's name or topic, which breaks its form.
	ErrInvalid = errors.New("invalid")
	// ErrNothing is a grant that reads no event under the topic.
	ErrNothing = errors.New("the grant gives no event under the topic")
	// ErrNotFound is an app, or a subscription, that is not there.
	ErrNotFound = errors.New("no such app or subscription")
	// ErrConflict is a subscription another app holds, or a client whose
	// revoke is in progress.
	ErrConflict = broker.ErrConflict
)

// subNameRE is the form of a subscription's name, the broker's.
var subNameRE = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)

// Broker is what the service needs of lodge's embedded broker
// (internal/broker).
type Broker interface {
	// SubscribeWith makes client's subscription sub, with the filters that
	// the callback answers inside the client's lifecycle lock.
	SubscribeWith(ctx context.Context, spec broker.Spec, filters func(ctx context.Context) ([]string, error)) ([]string, error)
	// Unsubscribe deletes client's subscription sub.
	Unsubscribe(ctx context.Context, client, sub string) error
	// Refilter replaces the filters of client's subscription sub, with the
	// ones the callback answers.
	Refilter(ctx context.Context, client, sub string, filters func(ctx context.Context, app, topic string) ([]string, error)) error
	// Revoke forgets a client: its connections and its consumers.
	Revoke(ctx context.Context, client string) error
	// Subscriptions are every subscription of every client.
	Subscriptions() []broker.Subscription
	// ClientSubscriptions are client's subscriptions.
	ClientSubscriptions(client string) []broker.Subscription
	// ConsumerState answers what 1 consumer takes, and how far it has got.
	ConsumerState(ctx context.Context, stream, consumer string) (broker.ConsumerState, error)
	// Resync publishes a resync event for each of client's consumers that
	// missed messages.
	Resync(ctx context.Context, client string) error
	// ResyncAll does Resync for every client.
	ResyncAll(ctx context.Context) error
	// CheckClients closes the connections whose token is gone.
	CheckClients(ctx context.Context)
}

// OAuth is what the service needs of lodge's OAuth server
// (internal/oauth).
type OAuth interface {
	// Check answers what a token gives, for a caller that needs a scope.
	Check(ctx context.Context, token, scope string) (oauth.Access, error)
	// Grant is the principal's grant for app.
	Grant(ctx context.Context, principal, app string) ([]oauth.Item, error)
	// Client is a live OAuth client.
	Client(ctx context.Context, id string) (oauth.Client, error)
}

// Apps is what the service needs of lodge's app registry.
type Apps interface {
	// Get answers 1 installed app.
	Get(id string) (apps.App, bool)
}

// Options configure the service.
type Options struct {
	Broker Broker
	OAuth  OAuth
	Apps   Apps
	// Log is where the service's log goes [discard].
	Log *slog.Logger
	// Now is the clock [time.Now].
	Now func() time.Time
}

// Service keeps the broker's subscriptions in step with the grants of the
// clients that hold them (ADR-0022; docs/specs/lodge-http.md, The broker).
type Service struct {
	o   Options
	log *slog.Logger

	// beforeActivate, when set, runs between a new subscription's
	// registration and the read that gives it its filters. A test sets it to
	// look at the subscription in that window.
	beforeActivate func()
}

// Subscription is 1 event subscription of a client.
type Subscription struct {
	// Name is the subscription's name.
	Name string `json:"name"`
	// App is the app whose events it takes.
	App string `json:"app"`
	// Topic is the topic prefix it asked for.
	Topic string `json:"topic"`
	// Stream and Consumer are the broker's durable pull consumer.
	Stream   string `json:"stream"`
	Consumer string `json:"consumer"`
	// Filters are the consumer's filters: the grant's effective read
	// prefixes of the topic, and the subscription's own resync subject.
	Filters []string `json:"filters"`
}

// New returns a service over o.
func New(o Options) *Service {
	log := o.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Service{o: o, log: log}
}

// now is the service's clock.
func (s *Service) now() time.Time {
	if s.o.Now == nil {
		return time.Now()
	}
	return s.o.Now()
}

// Tokens answers the broker's name of the client that token belongs to, and
// when the token expires: the broker's Options.Tokens (docs/specs/oauth.md,
// lodge's own callers).
func (s *Service) Tokens(ctx context.Context, token string) (string, time.Time, bool) {
	access, err := s.o.OAuth.Check(ctx, token, oauth.ScopeEvents)
	if err != nil {
		return "", time.Time{}, false
	}
	name, ok := ClientName(access.Client)
	if !ok {
		return "", time.Time{}, false
	}
	return name, access.Expires, true
}

// Subscribe gives the client of access the subscription sub, which takes the
// events under topic that the principal's grant reads. The filters are
// computed inside the broker's lifecycle lock, from the grant and the app's
// manifest, and a grant that reads nothing there refuses the subscription.
func (s *Service) Subscribe(ctx context.Context, access oauth.Access, sub, topic string) (Subscription, error) {
	if !subNameRE.MatchString(sub) {
		return Subscription{}, fmt.Errorf("subscription %q must match %s: %w", sub, subNameRE, ErrInvalid)
	}
	tokens, err := parseRequest(topic)
	if err != nil {
		return Subscription{}, err
	}
	app := tokens[0]
	if _, err := s.app(app); err != nil {
		return Subscription{}, err
	}
	client, ok := ClientName(access.Client)
	if !ok {
		return Subscription{}, fmt.Errorf("%q is not an OAuth client's id: %w", access.Client, ErrNotFound)
	}
	// The subscription is registered, and then activated, in 1 step inside the
	// broker's lifecycle lock: a request that deletes it and puts another 1 in
	// its place cannot slip between the 2.
	out := Subscription{Name: sub, App: app, Topic: topic, Stream: "app_" + app, Consumer: client + "-" + sub}
	installed, err := s.o.Broker.SubscribeWith(ctx, broker.Spec{Client: client, Sub: sub, App: app, Topic: topic,
		Activate: s.activate(access)},
		func(ctx context.Context) ([]string, error) {
			return s.subscriptionFilters(ctx, access.Principal, app, topic)
		})
	if err != nil {
		if errors.Is(err, broker.ErrCleanup) {
			// The subscription is still there, and it delivers nothing it
			// may: its client goes, so that nothing else on it does either.
			s.log.Warn("remove a subscription that could not be activated", "client", client, "sub", sub, "err", err)
			s.revoke(client)
		}
		if errors.Is(err, broker.ErrNotFound) {
			// The subscription went while it was activated, so there is
			// nothing left to clean up, and the client keeps its others.
			return Subscription{}, fmt.Errorf("%w: subscription %q was removed while it was activated", ErrNotFound, sub)
		}
		if errors.Is(err, broker.ErrTooManyFilters) {
			// The client's answer is 400: the grant is wider than 1
			// consumer, and a narrower topic is what fits.
			return Subscription{}, fmt.Errorf("%w: the grant is too wide for 1 subscription, and a subscription takes at most %d filters; narrow the topic: %w",
				ErrInvalid, broker.MaxFilters, err)
		}
		return Subscription{}, fmt.Errorf("subscribe %s to %s: its filters are not installed: %w", sub, app, err)
	}
	out.Filters = installed
	return out, nil
}

// subscriptionFilters reads the grant's filters for the topic, and refuses a
// grant that reads nothing there.
func (s *Service) subscriptionFilters(ctx context.Context, principal, app, topic string) ([]string, error) {
	filters, err := s.readFilters(ctx, principal, app, topic)
	if err != nil {
		return nil, err
	}
	if len(filters) == 0 {
		return nil, fmt.Errorf("the grant of %s gives no event under %s: %w", principal, topic, ErrNothing)
	}
	return filters, nil
}

// activate is the callback that installs a new subscription's filters: it runs
// inside the broker's lifecycle lock, after the subscription is registered.
func (s *Service) activate(access oauth.Access) func(ctx context.Context, app, topic string) ([]string, error) {
	return func(ctx context.Context, app, topic string) ([]string, error) {
		if s.beforeActivate != nil {
			s.beforeActivate()
		}
		// A client that OAuth revoked after lodge's token check holds no
		// subscription. A revocation after this check finds the subscription
		// registered, so its hook revokes it.
		if err := s.liveClient(ctx, access); err != nil {
			return nil, err
		}
		return s.subscriptionFilters(ctx, access.Principal, app, topic)
	}
}

// liveClient checks that the client of access is still the 1 OAuth serves: it
// exists, it is bound, it holds the events scope and it belongs to the token's
// principal. A client that fails a check answers ErrNotFound, which the
// gateway answers as 404: it was revoked after lodge's token check, so there
// is no subscription for it. A lookup that fails for another reason, such as
// a store that does not answer, says nothing about the client: its error goes
// back as it is, which the gateway answers as 503.
func (s *Service) liveClient(ctx context.Context, access oauth.Access) error {
	c, err := s.o.OAuth.Client(ctx, access.Client)
	if errors.Is(err, oauth.ErrNotFound) {
		return fmt.Errorf("the OAuth client of the token is gone: %w: %w", ErrNotFound, err)
	}
	if err != nil {
		return fmt.Errorf("read the OAuth client of the token: %w", err)
	}
	switch {
	case !c.Bound():
		return fmt.Errorf("client %s is bound to no principal: %w", c.ID, ErrNotFound)
	case c.Principal != access.Principal:
		return fmt.Errorf("client %s is not the token's principal's: %w", c.ID, ErrNotFound)
	case !hasScope(c.Scope, oauth.ScopeEvents):
		return fmt.Errorf("client %s has no scope %s: %w", c.ID, oauth.ScopeEvents, ErrNotFound)
	}
	return nil
}

// consumerFilters answers the filters that a subscription's consumer has: the
// ones that JetStream applies, and not the ones that a grant would give now.
func (s *Service) consumerFilters(ctx context.Context, sub Subscription) []string {
	out := []string{}
	state, err := s.o.Broker.ConsumerState(ctx, sub.Stream, sub.Consumer)
	if err != nil {
		s.log.Warn("read a subscription's consumer", "sub", sub.Name, "err", err)
		return out
	}
	if len(state.Filters) > 0 {
		return state.Filters
	}
	return out
}

// Unsubscribe deletes the client's subscription sub.
func (s *Service) Unsubscribe(ctx context.Context, access oauth.Access, sub string) error {
	if !subNameRE.MatchString(sub) {
		return fmt.Errorf("subscription %q must match %s: %w", sub, subNameRE, ErrInvalid)
	}
	client, ok := ClientName(access.Client)
	if !ok {
		return fmt.Errorf("%q is not an OAuth client's id: %w", access.Client, ErrNotFound)
	}
	if err := s.o.Broker.Unsubscribe(ctx, client, sub); err != nil {
		if errors.Is(err, broker.ErrNotFound) {
			return fmt.Errorf("subscription %q is not there: %w", sub, ErrNotFound)
		}
		return err
	}
	return nil
}

// List answers the client's subscriptions, each with the filters its consumer
// has: what JetStream applies, and not what a grant would give now.
func (s *Service) List(access oauth.Access) []Subscription {
	out := []Subscription{}
	client, ok := ClientName(access.Client)
	if !ok {
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	for _, sub := range s.o.Broker.ClientSubscriptions(client) {
		one := Subscription{Name: sub.Sub, App: sub.App, Topic: sub.Topic, Stream: sub.Stream, Consumer: sub.Consumer}
		one.Filters = s.consumerFilters(ctx, one)
		out = append(out, one)
	}
	return out
}

// GrantChanged refilters each of the app's subscriptions whose client
// belongs to principal: a grant that now reads nothing there leaves the
// consumer with its resync subject alone, and the subscription stays. A
// refilter that fails revokes the client, so that its delivery cannot outlive
// its grant.
func (s *Service) GrantChanged(principal, app string) {
	for _, sub := range s.o.Broker.Subscriptions() {
		if sub.App != app {
			continue
		}
		// Each subscription has its own budget: 1 that hangs does not take
		// the others' time.
		ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
		err := s.refilterOne(ctx, sub, principal)
		cancel()
		if err != nil {
			s.failClosed(sub.Client, sub.Sub, err)
		}
	}
}

// AppsChanged is called after a reload: an app's manifest may have changed,
// and with it what a grant reads there, its events, or its major version,
// which takes every grant item of the old one out of force
// (docs/specs/oauth.md, Principals, clients and grants). Every subscription
// is refiltered at once from its principal's grant, as a sweep would do
// within a minute; a refilter that fails closes as a grant change's does.
func (s *Service) AppsChanged() {
	for _, sub := range s.o.Broker.Subscriptions() {
		ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
		err := s.refilterAny(ctx, sub)
		cancel()
		if err != nil {
			s.failClosed(sub.Client, sub.Sub, err)
		}
	}
}

// refilterAny refilters 1 subscription from the grant of its client's
// principal. A client that cannot be named is an error: its subscription is
// never left with the filters it had.
func (s *Service) refilterAny(ctx context.Context, sub broker.Subscription) error {
	id, ok := ClientID(sub.Client)
	if !ok {
		return fmt.Errorf("client %q is not an OAuth client's id: %w", sub.Client, ErrNotFound)
	}
	c, err := s.o.OAuth.Client(ctx, id)
	if err != nil {
		return fmt.Errorf("read the OAuth client of %s: %w", sub.Client, err)
	}
	return s.o.Broker.Refilter(ctx, sub.Client, sub.Sub, s.refilter(c.Principal))
}

// refilterOne refilters 1 subscription when it belongs to principal. A
// failure to name its client is an error: a subscription that may be
// principal's is never left with the filters it had.
func (s *Service) refilterOne(ctx context.Context, sub broker.Subscription, principal string) error {
	mine, err := s.belongsTo(ctx, sub.Client, principal)
	if err != nil {
		return err
	}
	if !mine {
		return nil
	}
	return s.o.Broker.Refilter(ctx, sub.Client, sub.Sub, s.refilter(principal))
}

// failClosed answers a failed refilter. A subscription that is gone, a client
// whose revoke is under way and a grant too wide for its consumer have
// already stopped its delivery; any other error would leave delivery of
// events that the grant no longer reads, so the client is revoked. It reports
// whether it revoked the client.
func (s *Service) failClosed(client, sub string, err error) bool {
	for _, safe := range []error{broker.ErrNotFound, broker.ErrConflict, broker.ErrTooManyFilters} {
		if errors.Is(err, safe) {
			s.log.Warn("refilter a subscription after a grant change", "client", client, "sub", sub, "err", err)
			return false
		}
	}
	s.log.Warn("refilter a subscription after a grant change failed; revoking its client",
		"client", client, "sub", sub, "err", err)
	s.revoke(client)
	return true
}

// revoke revokes 1 broker client within its own budget, so that a hook or a
// sweep never waits on a store that does not answer.
func (s *Service) revoke(client string) {
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	if err := s.o.Broker.Revoke(ctx, client); err != nil {
		s.log.Warn("revoke a broker client", "client", client, "err", err)
	}
}

// belongsTo reports whether the client's OAuth client is bound to principal,
// and why it cannot be read when it cannot be: a subscription whose client
// cannot be named is never left with the filters it had.
func (s *Service) belongsTo(ctx context.Context, client, principal string) (bool, error) {
	id, ok := ClientID(client)
	if !ok {
		return false, fmt.Errorf("client %q is not an OAuth client's id: %w", client, ErrNotFound)
	}
	c, err := s.o.OAuth.Client(ctx, id)
	if err != nil {
		return false, fmt.Errorf("read the OAuth client of %s: %w", client, err)
	}
	return c.Principal == principal, nil
}

// refilter is the callback of a refilter: the principal's current grant of
// the app's events under the subscription's topic.
func (s *Service) refilter(principal string) func(ctx context.Context, app, topic string) ([]string, error) {
	return func(ctx context.Context, app, topic string) ([]string, error) {
		return s.readFilters(ctx, principal, app, topic)
	}
}

// Revoked revokes the broker's clients of the OAuth clients that go, which
// closes their connections and deletes their consumers.
func (s *Service) Revoked(principal string, clients []string) {
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	for _, id := range clients {
		name, ok := ClientName(id)
		if !ok {
			continue
		}
		if err := s.o.Broker.Revoke(ctx, name); err != nil {
			s.log.Warn("revoke a broker client", "client", name, "principal", principal, "err", err)
		}
	}
}

// Run sweeps every SweepEvery until ctx ends.
func (s *Service) Run(ctx context.Context) {
	tick := time.NewTicker(SweepEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if err := s.Sweep(ctx); err != nil {
				s.log.Warn("sweep the event subscriptions", "err", err)
			}
		}
	}
}

// Sweep runs 1 round: the resync of the consumers that missed messages, the
// connections whose token is gone, the filters of every subscription, and
// the broker clients whose OAuth client is gone or has lost the events
// scope. A missed hook, or a manifest that a reload changed, is corrected
// here.
func (s *Service) Sweep(ctx context.Context) error {
	start := s.now()
	var errs []error
	if err := s.o.Broker.ResyncAll(ctx); err != nil {
		errs = append(errs, err)
	}
	s.o.Broker.CheckClients(ctx)
	subs := s.o.Broker.Subscriptions()
	revoked := map[string]bool{}
	for _, sub := range subs {
		if revoked[sub.Client] {
			continue
		}
		id, ok := ClientID(sub.Client)
		if !ok {
			continue
		}
		c, err := s.o.OAuth.Client(ctx, id)
		switch {
		case errors.Is(err, oauth.ErrNotFound), err == nil && (!c.Bound() || !hasScope(c.Scope, oauth.ScopeEvents)):
			revoked[sub.Client] = true
			if err := s.o.Broker.Revoke(ctx, sub.Client); err != nil {
				errs = append(errs, fmt.Errorf("revoke %s: %w", sub.Client, err))
			}
			continue
		case err != nil:
			errs = append(errs, fmt.Errorf("read the client of %s: %w", sub.Client, err))
			continue
		}
		if err := s.o.Broker.Refilter(ctx, sub.Client, sub.Sub, s.refilter(c.Principal)); err != nil {
			// A sweep that lodge stops ends here: its cancelled refilters
			// say nothing about a grant, and must revoke no client.
			if ctx.Err() != nil {
				errs = append(errs, ctx.Err())
				break
			}
			if s.failClosed(sub.Client, sub.Sub, err) {
				revoked[sub.Client] = true
			}
		}
	}
	s.log.Debug("event sweep", "subscriptions", len(subs), "revoked", len(revoked), "took", s.now().Sub(start))
	return errors.Join(errs...)
}

// readFilters answers the filters that principal reads under topic in app.
func (s *Service) readFilters(ctx context.Context, principal, app, topic string) ([]string, error) {
	a, err := s.app(app)
	if err != nil {
		return nil, err
	}
	grant, err := s.o.OAuth.Grant(ctx, principal, app)
	if err != nil {
		return nil, fmt.Errorf("read the grant of %s for %s: %w", principal, app, err)
	}
	return Filters(a.Hearth.Events, grant, topic)
}

// app answers 1 installed, enabled app with its hearth manifest.
func (s *Service) app(id string) (apps.App, error) {
	a, ok := s.o.Apps.Get(id)
	if !ok || !a.Enabled() || a.Hearth == nil {
		return apps.App{}, fmt.Errorf("app %q is not installed with a hearth manifest: %w", id, ErrNotFound)
	}
	return a, nil
}

// hasScope reports whether a client's scope list holds scope.
func hasScope(scopes, scope string) bool {
	return slices.Contains(strings.Fields(scopes), scope)
}
