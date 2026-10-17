package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	inboundhttp "github.com/claudioed/task-interleaving/internal/adapters/inbound/http"
	"github.com/claudioed/task-interleaving/internal/application/outbox"
	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/application/usecases"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// lockedBuffer is a log sink safe for the worker goroutines.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func quietLogger() (*slog.Logger, *lockedBuffer) {
	logs := &lockedBuffer{}
	return slog.New(slog.NewTextHandler(logs, nil)), logs
}

func do(t *testing.T, method, url, body string, headers ...string) (int, string, http.Header) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw), resp.Header
}

// seedWorld gives SITE-1 a layout and an open-task pool through the ports, as
// the consumers will: from A-01 (0,0,0) the task at B-01 is 4 m away. It is
// also used by the Postgres integration tests.
func seedWorld(t *testing.T, ad adapters) {
	t.Helper()
	ctx := t.Context()
	steps := []error{
		ad.book.SaveZone(ctx, repository.Zone{ID: "z1", SiteCode: "SITE-1", ZoneCode: "FWD"}),
		ad.book.SaveLocation(ctx, "A-01", "z1"),
		ad.book.SaveGeometry(ctx, "A-01", 0, 0, 0),
		ad.book.SaveLocation(ctx, "B-01", "z1"),
		ad.book.SaveGeometry(ctx, "B-01", 4, 0, 0),
		ad.pool.Save(ctx, repository.Task{ID: "task-9c2e", Type: "PICK", SiteID: "SITE-1", FromLocation: "B-01", ToLocation: "B-01", State: repository.TaskAvailable}),
	}
	for _, err := range steps {
		if err != nil {
			t.Fatal(err)
		}
	}
}

// completeTask records that STN-07 completed task (claimed by it, ending at
// A-01) through the same use case the fulfillment consumer will call.
func completeTask(t *testing.T, ad adapters, task string) {
	t.Helper()
	ctx := t.Context()
	err := ad.pool.Save(ctx, repository.Task{
		ID: task, Type: "PICK", SiteID: "SITE-1", FromLocation: "A-01", ToLocation: "A-01",
		State: repository.TaskClaimed, ClaimedBy: "STN-07",
	})
	if err != nil {
		t.Fatal(err)
	}
	uc := &usecases.ApplyTaskCompleted{
		Intake: usecases.Intake{UoW: ad.uow, Processed: ad.processed}, TripWriter: ad.tripWriter(),
		Policies: ad.policies, Pool: ad.pool, Ranker: ad.ranker(), Clock: ad.clock, IDs: ad.ids,
	}
	if out, err := uc.Handle(ctx, "evt-"+task, usecases.TaskCompletion{TaskID: task}); err != nil || out != usecases.OutcomeApplied {
		t.Fatalf("completion = %s %v", out, err)
	}
}

const (
	declareBody = `{"maxDetourM":25,"maxCandidates":3,"hintTtlSeconds":1,"eligibleTaskTypes":["PICK"]}`
	simulation  = `{"stationId":"STN-07","fromLocation":"A-01"}`
)

// The in-memory development mode: no DATABASE_URL, no outbox, no relay, and
// the Idempotency-Key is not enforced; the whole API works end to end.
func TestInMemoryModeServesTheWholeFlow(t *testing.T) {
	ad, closeAdapters, err := buildAdapters(t.Context(), "", "", discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer closeAdapters()
	if ad.outbox != nil || ad.idempotency != nil {
		t.Fatal("in-memory mode has no outbox and no idempotency middleware")
	}
	seedWorld(t, ad)
	srv := httptest.NewServer(inboundhttp.NewRouter(buildServer(ad, &inboundhttp.Readiness{})))
	defer srv.Close()

	assertDeclareAndSimulate(t, srv.URL)
	assertReplaceNeedsIfMatch(t, srv.URL)
	if status, body, _ := do(t, http.MethodGet, srv.URL+"/hints", ""); status != http.StatusOK || body != "{\"items\":[]}\n" {
		t.Fatalf("hints = %d %q", status, body)
	}
	if status, _, _ := do(t, http.MethodGet, srv.URL+"/readyz", ""); status != http.StatusOK {
		t.Fatalf("readyz = %d", status)
	}
}

func assertDeclareAndSimulate(t *testing.T, base string) {
	t.Helper()
	status, body, hdr := do(t, http.MethodPut, base+"/sites/SITE-1/policy", declareBody)
	if status != http.StatusOK || hdr.Get("ETag") != `"1"` || !strings.Contains(body, `"version":1`) {
		t.Fatalf("declare = %d %s %v", status, body, hdr)
	}
	if status, body, _ := do(t, http.MethodPost, base+"/recommendations", simulation); status != http.StatusOK ||
		!strings.Contains(body, `"taskId":"task-9c2e"`) || !strings.Contains(body, `"detourM":4`) {
		t.Fatalf("recommend = %d %s", status, body)
	}
}

func assertReplaceNeedsIfMatch(t *testing.T, base string) {
	t.Helper()
	if status, body, _ := do(t, http.MethodPut, base+"/sites/SITE-1/policy", declareBody); status != http.StatusPreconditionFailed {
		t.Fatalf("replacing without If-Match = %d %s, want 412", status, body)
	}
	if status, _, hdr := do(t, http.MethodPut, base+"/sites/SITE-1/policy", declareBody, "If-Match", `"1"`); status != http.StatusOK || hdr.Get("ETag") != `"2"` {
		t.Fatalf("replacing with If-Match = %d %v", status, hdr)
	}
}

// The expiry worker is wired to the real ExpireHints over the adapters: an
// Active hint whose ttl passed is Expired without any claim or completion.
func TestExpiryWorkerExpiresHintsOverTheAdapters(t *testing.T) {
	ad := memoryAdapters()
	seedWorld(t, ad)
	srv := httptest.NewServer(inboundhttp.NewRouter(buildServer(ad, &inboundhttp.Readiness{})))
	defer srv.Close()
	if status, body, _ := do(t, http.MethodPut, srv.URL+"/sites/SITE-1/policy", declareBody); status != http.StatusOK {
		t.Fatalf("declare = %d %s", status, body)
	}
	completeTask(t, ad, "task-8a1f")
	if _, body, _ := do(t, http.MethodGet, srv.URL+"/hints?state=Active", ""); !strings.Contains(body, `"state":"Active"`) {
		t.Fatalf("no Active hint to expire: %s", body)
	}

	w := startExpiryWorker(ad.expireHints(), 10*time.Millisecond, discardLogger())
	defer stopWorkers(discardLogger(), []*worker{w})
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, body, _ := do(t, http.MethodGet, srv.URL+"/hints?state=Expired", ""); strings.Contains(body, `"state":"Expired"`) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the expiry worker never expired the hint")
}

// fakeStore is a relay Store that never has anything to send.
type fakeStore struct {
	mu     sync.Mutex
	drains int
}

func (s *fakeStore) Drain(ctx context.Context, _, _ int, _ func(context.Context, outbox.Message) error) (int, error) {
	s.mu.Lock()
	s.drains++
	s.mu.Unlock()
	return 0, ctx.Err()
}

func (s *fakeStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.drains
}

func TestOutboxRelayStartsOnlyWithAStoreAndBrokers(t *testing.T) {
	logger, logs := quietLogger()
	if w := startOutboxRelay(nil, []string{"b:9092"}, logger); w != nil {
		t.Fatal("no store (in-memory mode): no relay")
	}
	if w := startOutboxRelay(&fakeStore{}, nil, logger); w != nil {
		t.Fatal("no brokers: no relay")
	}
	if !strings.Contains(logs.String(), "outbox relay off") {
		t.Fatalf("the reason must be logged: %s", logs.String())
	}
}

func TestOutboxRelayRunsAndStops(t *testing.T) {
	t.Setenv("OUTBOX_RELAY_INTERVAL", "5ms")
	t.Setenv("OUTBOX_MAX_ATTEMPTS", "3")
	store := &fakeStore{}
	w := startOutboxRelay(store, []string{"127.0.0.1:1"}, discardLogger())
	if w == nil {
		t.Fatal("a store and brokers must start the relay")
	}
	waitUntil(t, func() bool { return store.count() >= 2 }, "the relay never polled twice")
	w.stop()
	select {
	case <-w.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the relay did not stop")
	}
}

// fakeSweeper counts the sweeps the expiry worker asks for.
type fakeSweeper struct {
	mu     sync.Mutex
	sweeps int
}

func (s *fakeSweeper) Handle(context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweeps++
	return 0, nil
}

func (s *fakeSweeper) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sweeps
}

func TestExpiryWorkerSweepsOnItsIntervalAndStops(t *testing.T) {
	sweeper := &fakeSweeper{}
	w := startExpiryWorker(sweeper, 5*time.Millisecond, discardLogger())
	waitUntil(t, func() bool { return sweeper.count() >= 3 }, "the expiry worker never swept three times")
	w.stop()
	select {
	case <-w.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the expiry worker did not stop")
	}
}

func waitUntil(t *testing.T, cond func() bool, failure string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatal(failure)
}

func TestAwait(t *testing.T) {
	logger, logs := quietLogger()
	await(logger, nil, time.Millisecond) // nil worker: no-op
	done := &worker{name: "done", done: make(chan struct{})}
	close(done.done)
	await(logger, done, time.Second)
	if logs.String() != "" {
		t.Fatalf("a stopped worker must not warn: %s", logs.String())
	}
	await(logger, &worker{name: "stuck", done: make(chan struct{})}, 10*time.Millisecond)
	if !strings.Contains(logs.String(), "worker did not stop") || !strings.Contains(logs.String(), "stuck") {
		t.Fatalf("a stuck worker must be reported: %s", logs.String())
	}
}

func TestRunRefusesABadExpiryIntervalBeforeDialing(t *testing.T) {
	t.Setenv("HINT_EXPIRY_INTERVAL", "soon")
	// A dead database: were it dialled first the boot would sit in the retry
	// backoff for 15 s instead of naming the real problem.
	t.Setenv("DATABASE_URL", "postgres://nobody:***@127.0.0.1:1/none?sslmode=disable&connect_timeout=1")
	start := time.Now()
	err := run()
	if err == nil || !strings.Contains(err.Error(), "HINT_EXPIRY_INTERVAL") || !strings.Contains(err.Error(), `"soon"`) {
		t.Fatalf("run() = %v, want a boot error naming HINT_EXPIRY_INTERVAL", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("the config error took %v: the database was dialled first", time.Since(start))
	}
}
