package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	inboundhttp "github.com/claudioed/task-interleaving/internal/adapters/inbound/http"
	"github.com/claudioed/task-interleaving/internal/application/ports"
)

// purgeClock is a fixed clock the retention tests read notBefore from.
type purgeClock struct{ now time.Time }

func (c purgeClock) Now() time.Time { return c.now }

// countingStore is a ports.IdempotencyStore whose PurgeExpired answers from
// a scripted list of results, one per call, and records every call.
type countingStore struct {
	ports.IdempotencyStore
	results []int
	err     error // returned with the LAST scripted result
	calls   atomic.Int32
	lastArg struct {
		notBefore atomic.Value // time.Time
		limit     atomic.Int32
	}
}

func (s *countingStore) PurgeExpired(_ context.Context, notBefore time.Time, limit int) (int, error) {
	i := int(s.calls.Add(1)) - 1
	s.lastArg.notBefore.Store(notBefore)
	s.lastArg.limit.Store(int32(limit))
	out := 0
	if i < len(s.results) {
		out = s.results[i]
	}
	if i == len(s.results)-1 {
		return out, s.err
	}
	return out, nil
}

func TestPurgeIdempotencyKeysBatchesUntilAShortBatch(t *testing.T) {
	store := &countingStore{results: []int{idempotencyPurgeBatch, idempotencyPurgeBatch, 12}}
	now := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)
	total, err := purgeIdempotencyKeys(context.Background(), store, purgeClock{now}, idempotencyPurgeBatch)
	if err != nil || total != 2*idempotencyPurgeBatch+12 {
		t.Fatalf("purge = %d %v, want %d", total, err, 2*idempotencyPurgeBatch+12)
	}
	if got := store.calls.Load(); got != 3 {
		t.Fatalf("calls = %d, want 3 (full, full, short)", got)
	}
	if got := store.lastArg.limit.Load(); got != int32(idempotencyPurgeBatch) {
		t.Fatalf("limit = %d, want %d", got, idempotencyPurgeBatch)
	}
	wantNotBefore := now.Add(-inboundhttp.IdempotencyWindow)
	if got := store.lastArg.notBefore.Load().(time.Time); !got.Equal(wantNotBefore) {
		t.Fatalf("notBefore = %v, want now - IdempotencyWindow (%v)", got, wantNotBefore)
	}
}

func TestPurgeIdempotencyKeysStopsAtTheFirstFailure(t *testing.T) {
	boom := errors.New("database gone")
	store := &countingStore{results: []int{idempotencyPurgeBatch, 3}, err: boom}
	total, err := purgeIdempotencyKeys(context.Background(), store, purgeClock{time.Now()}, idempotencyPurgeBatch)
	if !errors.Is(err, boom) || total != idempotencyPurgeBatch+3 {
		t.Fatalf("purge = %d %v, want the partial %d and the error", total, err, idempotencyPurgeBatch+3)
	}
}

func TestStartIdempotencyPurgeRunsOnePassAtStartAndStops(t *testing.T) {
	store := &countingStore{results: []int{0}}
	w := startIdempotencyPurge(store, purgeClock{time.Now()}, discardLogger())
	defer w.stop()
	deadline := time.After(5 * time.Second)
	for store.calls.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("the purge worker never ran its first pass")
		case <-time.After(5 * time.Millisecond):
		}
	}
	w.stop()
	select {
	case <-w.done:
	case <-time.After(workerDrainTimeout):
		t.Fatal("the purge worker did not stop")
	}
}
