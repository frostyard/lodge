package broker

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/nats-io/nats.go/jetstream"
)

// Refilter replaces the filters of client's subscription sub, with the ones
// the callback answers for its app and topic: the consumer keeps its
// position and the client's connections stay open, because its permissions
// do not change. An empty answer leaves the consumer with its resync subject
// alone, so its delivery stops and the subscription stays.
//
// A callback that fails, or that answers filters this broker cannot install,
// stops the consumer's delivery the same way before the error goes back: a
// failed refilter never leaves the events that a grant no longer reads.
//
// It runs inside the client's lifecycle lock, and a revoke of the client
// wins: it answers an error, and the consumer this call updated goes.
func (b *Broker) Refilter(ctx context.Context, client, sub string,
	filters func(ctx context.Context, app, topic string) ([]string, error)) error {
	if err := validateName("client", client); err != nil {
		return err
	}
	if err := validateName("sub", sub); err != nil {
		return err
	}
	unlock := b.lifecycle(client)
	defer unlock()
	return b.refilterLocked(ctx, client, sub, filters)
}

// ErrCleanup is a subscription that a failed activation could not remove.
var ErrCleanup = errors.New("the subscription is not removed")

// Activate installs a new subscription's filters, and removes the subscription
// when that fails, in 1 step under the client's lifecycle lock: a request that
// deletes the subscription and puts another 1 in its place between the 2 would
// otherwise lose its replacement to this call's cleanup.
//
// It removes nothing when the subscription is gone (ErrNotFound), when a
// revoke of the client is under way (ErrConflict, and the revoke deletes it),
// or when a revoke overtook the refilter (the consumer is deleted already). A
// cleanup that fails answers its error with ErrCleanup.
func (b *Broker) Activate(ctx context.Context, client, sub string,
	filters func(ctx context.Context, app, topic string) ([]string, error)) error {
	if err := validateName("client", client); err != nil {
		return err
	}
	if err := validateName("sub", sub); err != nil {
		return err
	}
	unlock := b.lifecycle(client)
	defer unlock()
	err := b.refilterLocked(ctx, client, sub, filters)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrConflict), errors.Is(err, errRefilterRevoked):
		// The subscription is gone, a revoke deletes it, or a revoke took the
		// consumer this call wrote: there is nothing to remove.
		return err
	}
	// The activation may have failed because its budget ran out: the
	// cleanup has a budget of its own.
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), revokeTimeout)
	defer cancel()
	if uerr := b.unsubscribeLocked(cctx, client, sub); uerr != nil && !errors.Is(uerr, ErrNotFound) {
		return fmt.Errorf("%w: %w: %w", err, ErrCleanup, uerr)
	}
	return err
}

// refilterLocked is Refilter under the client's lifecycle lock, which the
// caller holds.
func (b *Broker) refilterLocked(ctx context.Context, client, sub string,
	filters func(ctx context.Context, app, topic string) ([]string, error)) error {
	b.mu.Lock()
	epoch := b.epochs[client]
	revoking := b.inflight[client] > 0
	var ref consumerRef
	var topic string
	if c := b.clients[client]; c != nil && c.subs[sub] != nil {
		ref = *c.subs[sub]
		topic = c.topics[sub]
	}
	b.mu.Unlock()
	if revoking {
		return fmt.Errorf("refilter %s/%s: a revoke of the client is in progress: %w", client, sub, ErrConflict)
	}
	if ref.stream == "" {
		return fmt.Errorf("refilter %s: no subscription %q: %w", client, sub, ErrNotFound)
	}
	app := appOf(ref.stream)
	if topic == "" {
		topic = app
	}
	c, err := b.js.Consumer(ctx, ref.stream, ref.consumer)
	if errors.Is(err, jetstream.ErrConsumerNotFound) {
		return fmt.Errorf("refilter %s/%s: its consumer is gone: %w", client, sub, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("refilter %s/%s: %w", client, sub, err)
	}
	info, err := c.Info(ctx)
	if err != nil {
		return fmt.Errorf("refilter %s/%s: %w", client, sub, err)
	}
	// fail stops the consumer's delivery and answers the refilter's error: a
	// refilter that cannot install the grant's filters must never leave the
	// events that the grant no longer reads. The write that stops it must not
	// outlive a revoke either: the check after it is the success path's.
	fail := func(err error) error {
		b.stopDelivery(ctx, info.Config, app, ref)
		if rerr := b.deleteAfterRevoke(ctx, client, epoch, ref); rerr != nil {
			return fmt.Errorf("refilter %s/%s: %w", client, sub, rerr)
		}
		return fmt.Errorf("refilter %s/%s: %w", client, sub, err)
	}
	fs, err := filters(ctx, app, topic)
	if err != nil {
		return fail(err)
	}
	if len(fs) > 0 {
		if err := validateFilters(app, fs); err != nil {
			return fail(err)
		}
	}
	// The new filters are the callback's, and the consumer's own resync
	// subject: a filter list is never empty, which would mean every subject.
	next := append(slices.Clone(fs), ResyncSubject(app, ref.consumer))
	var tooMany error
	if err := validateCount(next); err != nil {
		// The filters it has may be wider than the new grant, so it never
		// keeps them: it fails closed, with its resync subject alone, and
		// its delivery stops until the topic is narrower.
		tooMany = fmt.Errorf("refilter %s/%s: its delivery stops: %w", client, sub, err)
		next = []string{ResyncSubject(app, ref.consumer)}
	}
	// The minute sweep refilters every subscription: an unchanged list writes
	// nothing.
	if sameFilters(consumerFilters(info.Config), next) {
		return tooMany
	}
	cfg := refilterConfig(info.Config, next, client, sub, topic)
	if _, err := b.js.CreateOrUpdateConsumer(ctx, ref.stream, cfg); err != nil {
		return fmt.Errorf("refilter %s/%s: %w", client, sub, err)
	}
	if err := b.deleteAfterRevoke(ctx, client, epoch, ref); err != nil {
		return fmt.Errorf("refilter %s/%s: %w", client, sub, err)
	}
	return tooMany
}

// errRefilterRevoked is a refilter that a revoke of its client overtook.
var errRefilterRevoked = errors.New("the client was revoked during the refilter")

// deleteAfterRevoke deletes ref when a revoke of client has run since epoch:
// the consumer a refilter just wrote is then nobody's, and a revoke that
// already deleted it must not leave the copy this call made.
func (b *Broker) deleteAfterRevoke(ctx context.Context, client string, epoch uint64, ref consumerRef) error {
	b.mu.Lock()
	moved := b.epochs[client] != epoch
	b.mu.Unlock()
	if !moved {
		return nil
	}
	dctx, cancel := context.WithTimeout(ctx, revokeTimeout)
	defer cancel()
	kept, err := b.deleteRefs(dctx, []consumerRef{ref})
	b.keepRefs(client, kept)
	if err != nil {
		return fmt.Errorf("%w, and its consumer is kept: %w", errRefilterRevoked, err)
	}
	return errRefilterRevoked
}

// stopDelivery sets a consumer's filters to its resync subject alone, so that
// a refilter which failed leaves no delivery of events that its client's grant
// no longer reads. It logs a failure: the caller's error is about the
// refilter.
func (b *Broker) stopDelivery(ctx context.Context, cfg jetstream.ConsumerConfig, app string, ref consumerRef) {
	only := []string{ResyncSubject(app, ref.consumer)}
	if sameFilters(consumerFilters(cfg), only) {
		return
	}
	// The caller's budget may be spent: this write has its own, and it must
	// not be cancelled with the call that failed.
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), revokeTimeout)
	defer cancel()
	cfg.FilterSubject = ""
	cfg.FilterSubjects = only
	if _, err := b.js.CreateOrUpdateConsumer(sctx, ref.stream, cfg); err != nil {
		b.log.Warn("stop a subscription's delivery after a failed refilter", "consumer", ref.consumer, "err", err)
	}
}

// refilterConfig is the consumer config of a refilter: cfg's own members,
// with the new filters, and no single filter subject, which the server refuses
// beside a list.
func refilterConfig(cfg jetstream.ConsumerConfig, next []string, client, sub, topic string) jetstream.ConsumerConfig {
	cfg.FilterSubject = ""
	cfg.FilterSubjects = next
	cfg.Metadata = map[string]string{MetadataClient: client, MetadataSub: sub, MetadataTopic: topic}
	return cfg
}

// consumerFilters answers the filters a consumer's config holds: its filter
// subjects, or its single filter subject.
func consumerFilters(cfg jetstream.ConsumerConfig) []string {
	if len(cfg.FilterSubjects) > 0 {
		return slices.Clone(cfg.FilterSubjects)
	}
	if cfg.FilterSubject != "" {
		return []string{cfg.FilterSubject}
	}
	return nil
}

// sameFilters reports whether 2 filter lists hold the same subjects.
func sameFilters(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as, bs := slices.Clone(a), slices.Clone(b)
	slices.Sort(as)
	slices.Sort(bs)
	return slices.Equal(as, bs)
}
