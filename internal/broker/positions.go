package broker

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/nats-io/nats.go/jetstream"
)

// positionsBucket is the key-value bucket that keeps each consumer's last known
// stream position: a lower bound of what its client has passed, which is what
// finds a gap that a pull moved the consumer past.
//
// It is lodge's own stream, in the hub account. A client's account imports only
// its own consumers' pull and ack subjects, and an app's only its own subjects
// and the inboxes, so neither reaches this bucket (docs/specs/lodge-http.md,
// The broker). Its name has no app_ prefix, so rebuild never takes it for an
// app's stream.
const positionsBucket = "lodge_positions"

// positionKey is ref's key in the bucket: its stream and its consumer.
func positionKey(ref consumerRef) string { return ref.stream + "." + ref.consumer }

// makePositions makes the bucket of the positions: 1 value per consumer, and no
// history beyond the last 1, because the record is a lower bound and an older
// 1 is the safe side.
func (b *Broker) makePositions(ctx context.Context) error {
	kv, err := b.js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:  positionsBucket,
		History: 1,
		Storage: jetstream.FileStorage,
	})
	if err != nil {
		return fmt.Errorf("make the bucket of the consumers' positions: %w", err)
	}
	b.mu.Lock()
	b.kv = kv
	b.mu.Unlock()
	return nil
}

// positionBucket is the bucket, or nil before a start has made it.
func (b *Broker) positionBucket() jetstream.KeyValue {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.kv
}

// putPosition writes ref's position to the bucket. A write that fails is
// logged: the bucket then holds an older, lower position, which sends a resync
// that was not needed rather than none that was.
func (b *Broker) putPosition(ctx context.Context, ref consumerRef, seq uint64) {
	kv := b.positionBucket()
	if kv == nil {
		return
	}
	if _, err := kv.Put(ctx, positionKey(ref), []byte(strconv.FormatUint(seq, 10))); err != nil {
		b.log.Warn("record a consumer's position", "consumer", ref.consumer, "err", err)
	}
}

// deletePosition drops ref's position from the bucket.
func (b *Broker) deletePosition(ctx context.Context, ref consumerRef) {
	kv := b.positionBucket()
	if kv == nil {
		return
	}
	if err := kv.Delete(ctx, positionKey(ref)); err != nil && !errors.Is(err, jetstream.ErrKeyNotFound) {
		b.log.Warn("drop a consumer's position", "consumer", ref.consumer, "err", err)
	}
}

// storedPosition answers the position that the bucket holds for ref, and
// whether it holds 1 at all.
func (b *Broker) storedPosition(ctx context.Context, ref consumerRef) (uint64, bool) {
	kv := b.positionBucket()
	if kv == nil {
		return 0, false
	}
	entry, err := kv.Get(ctx, positionKey(ref))
	if errors.Is(err, jetstream.ErrKeyNotFound) {
		return 0, false
	}
	if err != nil {
		b.log.Warn("read a consumer's position", "consumer", ref.consumer, "err", err)
		return 0, false
	}
	seq, perr := strconv.ParseUint(string(entry.Value()), 10, 64)
	if perr != nil {
		b.log.Warn("read a consumer's position", "consumer", ref.consumer, "err", perr)
		return 0, false
	}
	return seq, true
}

// position answers the last known stream position of ref, and 0 when lodge
// has none: a consumer with no record may have passed any gap, so it is taken
// to be at the stream's start, which is the safe side.
func (b *Broker) position(ref consumerRef) uint64 {
	b.posMu.Lock()
	defer b.posMu.Unlock()
	return b.positions[ref]
}

// setPosition is ref's position at a start, from the bucket, and writes
// nothing: the bucket is where it came from.
func (b *Broker) setPosition(ref consumerRef, seq uint64) {
	b.posMu.Lock()
	defer b.posMu.Unlock()
	b.positions[ref] = seq
}

// notePosition records seq as the last known stream position of ref, in memory
// and in the bucket.
func (b *Broker) notePosition(ctx context.Context, ref consumerRef, seq uint64) {
	b.posMu.Lock()
	b.positions[ref] = seq
	b.posMu.Unlock()
	b.putPosition(ctx, ref, seq)
}

// seedPosition records seq as ref's position when lodge has none for it, or
// when seq is lower than the 1 it has: a consumer that is made again never
// raises the record, because its consumer may hold events that its filters let
// it read.
func (b *Broker) seedPosition(ctx context.Context, ref consumerRef, seq uint64) {
	b.posMu.Lock()
	old, ok := b.positions[ref]
	if ok && old <= seq {
		b.posMu.Unlock()
		return
	}
	b.positions[ref] = seq
	b.posMu.Unlock()
	b.putPosition(ctx, ref, seq)
}

// forgetPosition drops what lodge knows of ref's position, in memory and in the
// bucket. It takes no resync lock: a revoke that deletes a consumer never waits
// for a resync check, and the bucket's delete has the caller's budget.
func (b *Broker) forgetPosition(ctx context.Context, ref consumerRef) {
	b.posMu.Lock()
	delete(b.positions, ref)
	b.posMu.Unlock()
	b.deletePosition(ctx, ref)
}
