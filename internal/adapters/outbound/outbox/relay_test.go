package outbox_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	relay "github.com/claudioed/task-interleaving/internal/adapters/outbound/outbox"
	"github.com/claudioed/task-interleaving/internal/application/outbox"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeStore is an in-memory relay.Store with the Drain contract of
// postgres.OutboxRepo: id order, one send at a time, stop at the first failure
// (recorded on the row and returned as an *outbox.SendFailure), rows at the
// attempt cap not claimed.
type fakeStore struct {
	mu   sync.Mutex
	rows []*fakeRow
}

type fakeRow struct {
	msg       outbox.Message
	attempts  int
	lastErr   string
	published bool
}

func (s *fakeStore) add(m outbox.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append(s.rows, &fakeRow{msg: m})
}

func (s *fakeStore) claimable(limit, maxAttempts int) []int {
	var ids []int
	for i, r := range s.rows {
		if r.published || (maxAttempts > 0 && r.attempts >= maxAttempts) {
			continue
		}
		if len(ids) == limit {
			break
		}
		ids = append(ids, i)
	}
	return ids
}

func (s *fakeStore) Drain(ctx context.Context, limit, maxAttempts int, send func(context.Context, outbox.Message) error) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	published := 0
	for _, i := range s.claimable(limit, maxAttempts) {
		r := s.rows[i]
		r.attempts++
		if err := send(ctx, r.msg); err != nil {
			r.lastErr = err.Error()
			return published, &outbox.SendFailure{RowID: int64(i + 1), Message: r.msg, Attempts: r.attempts, Err: err}
		}
		r.published, r.lastErr = true, ""
		published++
	}
	return published, nil
}

func (s *fakeStore) unpublished() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.rows {
		if !r.published {
			n++
		}
	}
	return n
}

func (s *fakeStore) lastError(i int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rows[i].lastErr
}

// recordingSink records every message it is sent, or fails with fail.
type recordingSink struct {
	mu   sync.Mutex
	msgs []outbox.Message
	fail error
}

func (s *recordingSink) Send(_ context.Context, msgs ...outbox.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	s.msgs = append(s.msgs, msgs...)
	return nil
}

func (s *recordingSink) ids() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.msgs))
	for i, m := range s.msgs {
		out[i] = m.EventID
	}
	return out
}

func (s *recordingSink) setFail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail = err
}

func seed(store *fakeStore, ids ...string) {
	for _, id := range ids {
		store.add(outbox.Message{EventID: id, Topic: "t", EventType: "T"})
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within 5s")
}

func TestRelayOnce_DrainsInOrderAndMarksPublished(t *testing.T) {
	store, sink := &fakeStore{}, &recordingSink{}
	seed(store, "a", "b")
	n, err := relay.NewRelay(store, sink, quiet()).RelayOnce(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("RelayOnce = %d, %v", n, err)
	}
	if got := sink.ids(); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("sent = %v", got)
	}
	if store.unpublished() != 0 {
		t.Errorf("unpublished = %d, want 0", store.unpublished())
	}
}

// The relay hands each row to the sink with its own topic and headers intact:
// that is what lets one relay serve the integration and the analytics topic.
func TestRelayOnce_KeepsEachRowsTopic(t *testing.T) {
	store, sink := &fakeStore{}, &recordingSink{}
	store.add(outbox.Message{EventID: "1", Topic: "x.events", EventType: "T"})
	store.add(outbox.Message{EventID: "1", Topic: "x.analytics", EventType: "T"})
	if _, err := relay.NewRelay(store, sink, quiet()).RelayOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sink.msgs) != 2 || sink.msgs[0].Topic != "x.events" || sink.msgs[1].Topic != "x.analytics" {
		t.Fatalf("sent = %+v", sink.msgs)
	}
}

// A full batch must be followed IMMEDIATELY by another pass: with a 1h interval
// and batch size 2, 5 rows only drain promptly if the relay does not sleep
// between full batches.
func TestRun_FullBatchIsFollowedImmediatelyByAnotherPass(t *testing.T) {
	store, sink := &fakeStore{}, &recordingSink{}
	seed(store, "1", "2", "3", "4", "5")
	r := relay.NewRelay(store, sink, quiet(), relay.WithInterval(time.Hour), relay.WithBatchSize(2))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	waitFor(t, func() bool { return store.unpublished() == 0 })
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run err = %v, want context.Canceled", err)
	}
	if got := sink.ids(); len(got) != 5 {
		t.Errorf("sent %v, want all 5 in order", got)
	}
}

func TestRun_PicksUpNewRowsEveryInterval(t *testing.T) {
	store, sink := &fakeStore{}, &recordingSink{}
	r := relay.NewRelay(store, sink, quiet(), relay.WithInterval(10*time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = r.Run(ctx) }()

	time.Sleep(30 * time.Millisecond) // empty passes
	seed(store, "late")
	waitFor(t, func() bool { return len(sink.ids()) == 1 })
}

// A failing broker leaves the row unpublished; once it recovers the SAME
// message (same persisted id) is republished.
func TestRun_RetriesAFailedPassAndRepublishesTheSameID(t *testing.T) {
	store, sink := &fakeStore{}, &recordingSink{}
	sink.setFail(errors.New("broker down"))
	seed(store, "evt-1")
	r := relay.NewRelay(store, sink, quiet(), relay.WithInterval(10*time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = r.Run(ctx) }()

	waitFor(t, func() bool { return store.lastError(0) != "" })
	if store.unpublished() != 1 {
		t.Fatal("row was marked published despite the send failure")
	}
	sink.setFail(nil)
	waitFor(t, func() bool { return store.unpublished() == 0 })
	if got := sink.ids(); len(got) != 1 || got[0] != "evt-1" {
		t.Errorf("sent = %v, want [evt-1]", got)
	}
}

func TestRun_StopsPromptlyOnCancel(t *testing.T) {
	r := relay.NewRelay(&fakeStore{}, &recordingSink{}, quiet(), relay.WithInterval(time.Hour))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run err = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run ignored cancellation")
	}
}

func TestRun_ReturnsImmediatelyForAnAlreadyCancelledContext(t *testing.T) {
	store := &fakeStore{}
	seed(store, "x")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := relay.NewRelay(store, &recordingSink{}, quiet()).Run(ctx)
	if !errors.Is(err, context.Canceled) || store.unpublished() != 1 {
		t.Fatalf("Run = %v, unpublished %d; want Canceled and an untouched outbox", err, store.unpublished())
	}
}

func TestOptionsIgnoreNonPositiveValues(t *testing.T) {
	store, sink := &fakeStore{}, &recordingSink{}
	seed(store, "a", "b", "c")
	// WithBatchSize(0)/(-1) must keep the default (100), so one pass drains all 3.
	r := relay.NewRelay(store, sink, nil, relay.WithBatchSize(0), relay.WithBatchSize(-1), relay.WithInterval(0), relay.WithInterval(-time.Second))
	if n, err := r.RelayOnce(context.Background()); err != nil || n != 3 {
		t.Fatalf("RelayOnce = %d, %v; want 3", n, err)
	}
}

func TestDefaults(t *testing.T) {
	if relay.DefaultInterval != time.Second || relay.DefaultBatchSize != 100 || relay.DefaultMaxAttempts != 20 {
		t.Fatalf("defaults = %v, %d, %d", relay.DefaultInterval, relay.DefaultBatchSize, relay.DefaultMaxAttempts)
	}
}
