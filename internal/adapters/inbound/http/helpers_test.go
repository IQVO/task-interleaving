package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	inboundhttp "github.com/claudioed/task-interleaving/internal/adapters/inbound/http"
	"github.com/claudioed/task-interleaving/internal/adapters/outbound/memory"
	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/application/usecases"
	"github.com/claudioed/task-interleaving/internal/domain/policy"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
)

var t0 = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

// testClock is a clock the tests move by hand; safe for the server goroutines.
type testClock struct{ ns atomic.Int64 }

func newClock() *testClock {
	c := &testClock{}
	c.ns.Store(t0.UnixNano())
	return c
}

func (c *testClock) Now() time.Time { return time.Unix(0, c.ns.Load()).UTC() }

func (c *testClock) Advance(d time.Duration) { c.ns.Add(int64(d)) }

// seqIDs mints hint-test-001, hint-test-002, ...
type seqIDs struct{ n atomic.Int32 }

func (s *seqIDs) NewHintID() string { return fmt.Sprintf("hint-test-%03d", s.n.Add(1)) }

var errDB = errors.New("db down")

// repoConflict is the error a version-guarded save returns when it loses a race.
var repoConflict = repository.ErrConcurrentModification

// equalJSON compares two decoded JSON values.
func equalJSON(a, b any) bool { return reflect.DeepEqual(a, b) }

// failure is an injectable error the server goroutines read safely.
type failure struct{ err atomic.Pointer[error] }

func (f *failure) set(err error) {
	if err == nil {
		f.err.Store(nil)
		return
	}
	f.err.Store(&err)
}

func (f *failure) get() error {
	if p := f.err.Load(); p != nil {
		return *p
	}
	return nil
}

// faultyPolicies wraps the memory policy repository and injects errors.
type faultyPolicies struct {
	*memory.PolicyRepo
	getFail, saveFail failure
}

func (r *faultyPolicies) Get(ctx context.Context, site policy.SiteID) (repository.PolicyRecord, error) {
	if err := r.getFail.get(); err != nil {
		return repository.PolicyRecord{}, err
	}
	return r.PolicyRepo.Get(ctx, site)
}

func (r *faultyPolicies) Save(ctx context.Context, rec repository.PolicyRecord, loaded int64) error {
	if err := r.saveFail.get(); err != nil {
		return err
	}
	return r.PolicyRepo.Save(ctx, rec, loaded)
}

// faultyTrips wraps the memory station repository: it counts the station
// lookups (one per simulation) and injects errors.
type faultyTrips struct {
	*memory.StationTripRepo
	gets                           atomic.Int32
	getFail, hintFail, listHintErr failure
}

func (r *faultyTrips) Get(ctx context.Context, id stationtrip.StationID) (*stationtrip.StationTrip, error) {
	r.gets.Add(1)
	if err := r.getFail.get(); err != nil {
		return nil, err
	}
	return r.StationTripRepo.Get(ctx, id)
}

func (r *faultyTrips) GetHint(ctx context.Context, id stationtrip.HintID) (repository.HintRecord, error) {
	if err := r.hintFail.get(); err != nil {
		return repository.HintRecord{}, err
	}
	return r.StationTripRepo.GetHint(ctx, id)
}

func (r *faultyTrips) ListHints(ctx context.Context, f repository.HintFilter, after stationtrip.HintID, limit int) ([]repository.HintRecord, error) {
	if err := r.listHintErr.get(); err != nil {
		return nil, err
	}
	return r.StationTripRepo.ListHints(ctx, f, after, limit)
}

// faultyStore wraps the memory idempotency store with injectable behaviour.
type faultyStore struct {
	*memory.IdempotencyStore
	lookupFail, saveFail failure
	// beforeSave runs before the first Save of a key lands, to play the
	// request that wins a race.
	beforeSave func(rec repository.IdempotencyRecord)
	saves      atomic.Int32
}

func (s *faultyStore) Lookup(ctx context.Context, key string) (repository.IdempotencyRecord, bool, error) {
	if err := s.lookupFail.get(); err != nil {
		return repository.IdempotencyRecord{}, false, err
	}
	return s.IdempotencyStore.Lookup(ctx, key)
}

func (s *faultyStore) Save(ctx context.Context, rec repository.IdempotencyRecord) error {
	s.saves.Add(1)
	if err := s.saveFail.get(); err != nil {
		return err
	}
	if s.beforeSave != nil {
		s.beforeSave(rec)
	}
	return s.IdempotencyStore.Save(ctx, rec)
}

// world is what the consumers (step 5) feed: the in-memory ports the tests seed.
type world struct {
	clock     *testClock
	policies  *faultyPolicies
	trips     *faultyTrips
	pool      *memory.TaskPool
	book      *memory.LocationBook
	events    *memory.EventLog
	store     *faultyStore
	uow       *memory.UnitOfWork
	completed *usecases.ApplyTaskCompleted
	claimed   *usecases.ApplyTaskClaimed
	expire    *usecases.ExpireHints
}

func newWorld() *world {
	clock := newClock()
	policies := &faultyPolicies{PolicyRepo: memory.NewPolicyRepo()}
	trips := &faultyTrips{StationTripRepo: memory.NewStationTripRepo()}
	pool, book := memory.NewTaskPool(), memory.NewLocationBook()
	events, processed := memory.NewEventLog(), memory.NewProcessedEventRepo()
	uow := memory.NewUnitOfWork(policies.PolicyRepo, trips.StationTripRepo, pool, book, events, processed)
	intake := usecases.Intake{UoW: uow, Processed: processed}
	writer := usecases.TripWriter{Trips: trips, Events: events}
	ranker := usecases.Ranker{Pool: pool, Book: book}
	return &world{
		clock: clock, policies: policies, trips: trips, pool: pool, book: book, events: events, uow: uow,
		store: &faultyStore{IdempotencyStore: memory.NewIdempotencyStore()},
		completed: &usecases.ApplyTaskCompleted{
			Intake: intake, TripWriter: writer, Policies: policies, Pool: pool, Ranker: ranker, Clock: clock, IDs: &seqIDs{},
		},
		claimed: &usecases.ApplyTaskClaimed{Intake: intake, TripWriter: writer, Pool: pool, Clock: clock},
		expire:  &usecases.ExpireHints{TripWriter: writer, UoW: uow, Clock: clock},
	}
}

func (w *world) server(idempotent bool) (*inboundhttp.Server, *inboundhttp.Readiness) {
	ranker := usecases.Ranker{Pool: w.pool, Book: w.book}
	readiness := &inboundhttp.Readiness{}
	s := &inboundhttp.Server{
		DeclarePolicy: &usecases.DeclarePolicy{Policies: w.policies, Events: w.events, UoW: w.uow, Clock: w.clock},
		GetPolicy:     &usecases.GetPolicy{Policies: w.policies},
		ListHints:     &usecases.ListHints{Trips: w.trips},
		GetHint:       &usecases.GetHint{Trips: w.trips},
		GetStation:    &usecases.GetStation{Trips: w.trips},
		Recommend:     &usecases.Recommend{Trips: w.trips, Policies: w.policies, Ranker: ranker},
		Readiness:     readiness,
	}
	if idempotent {
		s.Idempotency = inboundhttp.NewIdempotency(w.store, w.uow, w.clock).Middleware
	}
	return s, readiness
}

type fixture struct {
	*world
	readiness *inboundhttp.Readiness
	server    *httptest.Server
}

// newFixture serves the API over in-memory adapters without the Idempotency-Key
// middleware (the in-memory development mode).
func newFixture(t *testing.T) *fixture { return build(t, false) }

// newIdempotentFixture serves the API with the middleware on its POST.
func newIdempotentFixture(t *testing.T) *fixture { return build(t, true) }

func build(t *testing.T, idempotent bool) *fixture {
	t.Helper()
	w := newWorld()
	s, readiness := w.server(idempotent)
	srv := httptest.NewServer(inboundhttp.NewRouter(s))
	t.Cleanup(srv.Close)
	return &fixture{world: w, readiness: readiness, server: srv}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// seedWorld gives SITE-1 the layout and the open-task pool the simulations read:
// from A-01 (0,0,0) the task at B-01 is 4.0 m away, the one at B-02 10.0 m, the
// one at FAR-01 100 m; NOGEO has no geometry and the PACK task is not PICK or
// REPLENISH.
func (f *fixture) seedWorld(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	must(t, f.book.SaveZone(ctx, repository.Zone{ID: "z1", SiteCode: "SITE-1", ZoneCode: "FWD"}))
	points := map[string][3]float64{"A-01": {0, 0, 0}, "B-01": {4, 0, 0}, "B-02": {10, 0, 0}, "FAR-01": {100, 0, 0}, "REMOTE-01": {1000, 0, 0}}
	for code, p := range points {
		must(t, f.book.SaveLocation(ctx, code, "z1"))
		must(t, f.book.SaveGeometry(ctx, code, p[0], p[1], p[2]))
	}
	must(t, f.book.SaveLocation(ctx, "NOGEO", "z1"))
	f.available(t, "task-9c2e", "PICK", "B-01")
	f.available(t, "task-9d10", "REPLENISH", "B-02")
	f.available(t, "task-far", "PICK", "FAR-01")
	f.available(t, "task-nogeo", "PICK", "NOGEO")
	f.available(t, "task-pack", "PACK", "B-01")
}

func (f *fixture) available(t *testing.T, id, typ, from string) {
	t.Helper()
	must(t, f.pool.Save(t.Context(), repository.Task{
		ID: id, Type: typ, SiteID: "SITE-1", FromLocation: from, ToLocation: from, State: repository.TaskAvailable,
	}))
}

const policyBody = `{"maxDetourM":25,"maxCandidates":3,"hintTtlSeconds":90,"eligibleTaskTypes":["PICK","REPLENISH"],"verticalWeight":1.5}`

// declare declares the policy of SITE-1 (version 1).
func (f *fixture) declare(t *testing.T) {
	t.Helper()
	f.mustCall(t, http.MethodPut, "/sites/SITE-1/policy", policyBody, http.StatusOK)
}

// complete records a completed task of the station at A-01, which issues a
// hint when the site declared a policy and candidates exist.
func (f *fixture) complete(t *testing.T, station, task string) {
	t.Helper()
	ctx := t.Context()
	must(t, f.pool.Save(ctx, repository.Task{
		ID: task, Type: "PICK", SiteID: "SITE-1", FromLocation: "A-01", ToLocation: "A-01",
		State: repository.TaskClaimed, ClaimedBy: station,
	}))
	out, err := f.completed.Handle(ctx, "evt-"+task, usecases.TaskCompletion{TaskID: task, At: f.clock.Now()})
	must(t, err)
	if out != usecases.OutcomeApplied {
		t.Fatalf("completion of %s = %s", task, out)
	}
}

type response struct {
	status int
	header http.Header
	body   map[string]any
	raw    string
}

func (r response) contentType() string { return r.header.Get("Content-Type") }

// call performs a request with optional extra headers (key, value, ...).
func (f *fixture) call(t *testing.T, method, path, body string, headers ...string) response {
	t.Helper()
	var reader io.Reader = http.NoBody
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, f.server.URL+path, reader)
	must(t, err)
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	must(t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := response{status: resp.StatusCode, header: resp.Header, raw: string(raw)}
	_ = json.Unmarshal(raw, &out.body)
	return out
}

// mustCall performs a setup request and fails unless it returned want.
func (f *fixture) mustCall(t *testing.T, method, path, body string, want int, headers ...string) response {
	t.Helper()
	r := f.call(t, method, path, body, headers...)
	if r.status != want {
		t.Fatalf("%s %s = %d %s, want %d", method, path, r.status, r.raw, want)
	}
	return r
}

// expectProblem asserts an RFC 7807 response with the given status and slug.
func expectProblem(t *testing.T, r response, status int, slug string) {
	t.Helper()
	if r.status != status || r.contentType() != "application/problem+json" ||
		r.body["type"] != "https://errors.task-interleaving.warehouse-systems.dev/"+slug ||
		r.body["status"] != float64(status) || r.body["title"] == "" || r.body["detail"] == "" || r.body["instance"] == nil {
		t.Fatalf("got %d %s %s, want %d problem %s", r.status, r.contentType(), r.raw, status, slug)
	}
}

// parallel runs fn n times at once and waits.
func parallel(n int, fn func(i int)) {
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fn(i)
		}()
	}
	wg.Wait()
}
