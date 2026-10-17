package usecases_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/claudioed/task-interleaving/internal/adapters/outbound/memory"
	"github.com/claudioed/task-interleaving/internal/application/ports"
	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/application/usecases"
	"github.com/claudioed/task-interleaving/internal/domain/policy"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
	"github.com/claudioed/task-interleaving/internal/domain/travel"
)

var (
	errBoom = errors.New("boom")
	t0      = time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	bg      = context.Background()
)

type fixedClock struct{}

func (fixedClock) Now() time.Time { return t0 }

type seqIDs struct{ n int }

func (s *seqIDs) NewHintID() string {
	s.n++
	return "hint-" + string(rune('0'+s.n))
}

// faults says which "Port.Method" calls fail. always[name] fails every call
// with that error; nth[name] fails only the n-th call (1-based) with errBoom.
type faults struct {
	always map[string]error
	nth    map[string]int
	calls  map[string]int
}

func newFaults() *faults {
	return &faults{always: map[string]error{}, nth: map[string]int{}, calls: map[string]int{}}
}

func (f *faults) hit(name string) error {
	f.calls[name]++
	if n, ok := f.nth[name]; ok && f.calls[name] == n {
		return errBoom
	}
	return f.always[name]
}

// env wires the use cases over the in-memory adapters, each wrapped so any
// single method can be made to fail.
type env struct {
	faults    *faults
	policies  ports.PolicyRepo
	trips     ports.StationTripRepo
	pool      ports.TaskPool
	book      ports.LocationBook
	events    ports.EventSink
	log       *memory.EventLog
	processed ports.ProcessedEvents
	uow       *memory.UnitOfWork
	intake    usecases.Intake
	writer    usecases.TripWriter
	ranker    usecases.Ranker
}

func newEnv() *env {
	f := newFaults()
	policies, trips := memory.NewPolicyRepo(), memory.NewStationTripRepo()
	pool, book := memory.NewTaskPool(), memory.NewLocationBook()
	log, processed := memory.NewEventLog(), memory.NewProcessedEventRepo()
	e := &env{
		faults:    f,
		policies:  &failPolicies{policies, f},
		trips:     &failTrips{trips, f},
		pool:      &failPool{pool, f},
		book:      &failBook{book, f},
		events:    &failEvents{log, f},
		log:       log,
		processed: &failProcessed{processed, f},
		uow:       memory.NewUnitOfWork(policies, trips, pool, book, log, processed),
	}
	e.intake = usecases.Intake{UoW: e.uow, Processed: e.processed}
	e.writer = usecases.TripWriter{Trips: e.trips, Events: e.events}
	e.ranker = usecases.Ranker{Pool: e.pool, Book: e.book}
	return e
}

func (e *env) fail(names ...string) {
	for _, n := range names {
		e.faults.always[n] = errBoom
	}
}

// failNth makes only the n-th call of name from now on fail.
func (e *env) failNth(name string, n int) { e.faults.nth[name] = e.faults.calls[name] + n }

func (e *env) declare() *usecases.DeclarePolicy {
	return &usecases.DeclarePolicy{Policies: e.policies, Events: e.events, UoW: e.uow, Clock: fixedClock{}}
}

func (e *env) available() *usecases.ApplyTaskAvailable {
	return &usecases.ApplyTaskAvailable{Intake: e.intake, Pool: e.pool}
}

func (e *env) claimed() *usecases.ApplyTaskClaimed {
	return &usecases.ApplyTaskClaimed{Intake: e.intake, TripWriter: e.writer, Pool: e.pool, Clock: fixedClock{}}
}

func (e *env) completed() *usecases.ApplyTaskCompleted {
	return &usecases.ApplyTaskCompleted{
		Intake: e.intake, TripWriter: e.writer, Policies: e.policies, Pool: e.pool,
		Ranker: e.ranker, Clock: fixedClock{}, IDs: &seqIDs{},
	}
}

func (e *env) recommend() *usecases.Recommend {
	return &usecases.Recommend{Trips: e.trips, Policies: e.policies, Ranker: e.ranker}
}

// seed fills the local copies directly: site S1 with locations L0 (origin), A
// and B, a policy (v1), the open tasks T-1 (at A) and T-2 (at B), and task T0
// claimed by STN-1 and ending at L0.
func (e *env) seed(t *testing.T) {
	t.Helper()
	mustNil(t,
		e.book.SaveZone(bg, repository.Zone{ID: "z1", SiteCode: "S1", ZoneCode: "FWD"}),
		e.book.SaveLocation(bg, "L0", "z1"), e.book.SaveGeometry(bg, "L0", 0, 0, 0),
		e.book.SaveLocation(bg, "A", "z1"), e.book.SaveGeometry(bg, "A", 3, 0, 0),
		e.book.SaveLocation(bg, "B", "z1"), e.book.SaveGeometry(bg, "B", 3, 4, 0),
		e.pool.Save(bg, repository.Task{ID: "T0", Type: "PICK", SiteID: "S1", FromLocation: "L0", ToLocation: "L0", State: repository.TaskClaimed, ClaimedBy: "STN-1"}),
		e.pool.Save(bg, repository.Task{ID: "T-1", Type: "PICK", SiteID: "S1", FromLocation: "A", ToLocation: "A", State: repository.TaskAvailable}),
		e.pool.Save(bg, repository.Task{ID: "T-2", Type: "PICK", SiteID: "S1", FromLocation: "B", ToLocation: "B", State: repository.TaskAvailable}),
	)
	p, err := policy.New("S1", policy.Settings{
		MaxDetourM: 25, MaxCandidates: 3, HintTTL: 90 * time.Second,
		EligibleTaskTypes: []policy.TaskType{policy.TaskPick}, VerticalWeight: 1,
	})
	mustNil(t, err, e.policies.Save(bg, repository.PolicyRecord{Policy: p, DeclaredAt: t0}, 0))
}

// seedHinted is seed plus the completion of T0, which issues hint-1.
func (e *env) seedHinted(t *testing.T) {
	t.Helper()
	e.seed(t)
	out, err := e.completed().Handle(bg, "evt-seed", usecases.TaskCompletion{TaskID: "T0"})
	if err != nil || out != usecases.OutcomeApplied {
		t.Fatalf("seed completion = %v %v", out, err)
	}
}

func mustNil(t *testing.T, errs ...error) {
	t.Helper()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

// completedTrip is STN-1 having completed T-prev at L0 one minute before the
// fixed clock, with no hint.
func completedTrip(t *testing.T) *stationtrip.StationTrip {
	t.Helper()
	trip, err := stationtrip.New("STN-1")
	mustNil(t, err)
	pos := stationtrip.Position{Site: "S1", Place: travel.Place{Location: "L0", Point: &travel.Point{}}}
	_, err = trip.RecordCompletion("T-prev", t0.Add(-time.Minute), pos)
	mustNil(t, err)
	return trip
}

type failPolicies struct {
	ports.PolicyRepo
	f *faults
}

func (p *failPolicies) Get(ctx context.Context, site policy.SiteID) (repository.PolicyRecord, error) {
	if err := p.f.hit("PolicyRepo.Get"); err != nil {
		return repository.PolicyRecord{}, err
	}
	return p.PolicyRepo.Get(ctx, site)
}

func (p *failPolicies) Save(ctx context.Context, rec repository.PolicyRecord, v int64) error {
	if err := p.f.hit("PolicyRepo.Save"); err != nil {
		return err
	}
	return p.PolicyRepo.Save(ctx, rec, v)
}

type failTrips struct {
	ports.StationTripRepo
	f *faults
}

func (r *failTrips) Get(ctx context.Context, id stationtrip.StationID) (*stationtrip.StationTrip, error) {
	if err := r.f.hit("StationTripRepo.Get"); err != nil {
		return nil, err
	}
	return r.StationTripRepo.Get(ctx, id)
}

func (r *failTrips) Save(ctx context.Context, trip *stationtrip.StationTrip, v int64) error {
	if err := r.f.hit("StationTripRepo.Save"); err != nil {
		return err
	}
	return r.StationTripRepo.Save(ctx, trip, v)
}

func (r *failTrips) ExpiredActive(ctx context.Context, now time.Time, limit int) ([]stationtrip.StationID, error) {
	if err := r.f.hit("StationTripRepo.ExpiredActive"); err != nil {
		return nil, err
	}
	return r.StationTripRepo.ExpiredActive(ctx, now, limit)
}

func (r *failTrips) ListHints(ctx context.Context, f repository.HintFilter, after stationtrip.HintID, limit int) ([]repository.HintRecord, error) {
	if err := r.f.hit("StationTripRepo.ListHints"); err != nil {
		return nil, err
	}
	return r.StationTripRepo.ListHints(ctx, f, after, limit)
}

type failPool struct {
	ports.TaskPool
	f *faults
}

func (p *failPool) Get(ctx context.Context, id string) (repository.Task, bool, error) {
	if err := p.f.hit("TaskPool.Get"); err != nil {
		return repository.Task{}, false, err
	}
	return p.TaskPool.Get(ctx, id)
}

func (p *failPool) Save(ctx context.Context, t repository.Task) error {
	if err := p.f.hit("TaskPool.Save"); err != nil {
		return err
	}
	return p.TaskPool.Save(ctx, t)
}

func (p *failPool) Available(ctx context.Context, site string) ([]repository.Task, error) {
	if err := p.f.hit("TaskPool.Available"); err != nil {
		return nil, err
	}
	return p.TaskPool.Available(ctx, site)
}

type failBook struct {
	ports.LocationBook
	f *faults
}

func (b *failBook) Places(ctx context.Context, codes []string) (map[string]repository.Place, error) {
	if err := b.f.hit("LocationBook.Places"); err != nil {
		return nil, err
	}
	return b.LocationBook.Places(ctx, codes)
}

func (b *failBook) SaveZone(ctx context.Context, z repository.Zone) error {
	if err := b.f.hit("LocationBook.SaveZone"); err != nil {
		return err
	}
	return b.LocationBook.SaveZone(ctx, z)
}

func (b *failBook) SaveLocation(ctx context.Context, code, zone string) error {
	if err := b.f.hit("LocationBook.SaveLocation"); err != nil {
		return err
	}
	return b.LocationBook.SaveLocation(ctx, code, zone)
}

func (b *failBook) DecommissionLocation(ctx context.Context, code string) error {
	if err := b.f.hit("LocationBook.DecommissionLocation"); err != nil {
		return err
	}
	return b.LocationBook.DecommissionLocation(ctx, code)
}

func (b *failBook) SaveGeometry(ctx context.Context, code string, x, y, z float64) error {
	if err := b.f.hit("LocationBook.SaveGeometry"); err != nil {
		return err
	}
	return b.LocationBook.SaveGeometry(ctx, code, x, y, z)
}

type failEvents struct {
	ports.EventSink
	f *faults
}

func (s *failEvents) PublishTripEvents(ctx context.Context, ev ...stationtrip.Event) error {
	if err := s.f.hit("EventSink.PublishTripEvents"); err != nil {
		return err
	}
	return s.EventSink.PublishTripEvents(ctx, ev...)
}

func (s *failEvents) PublishPolicyDeclared(ctx context.Context, d repository.PolicyDeclaration) error {
	if err := s.f.hit("EventSink.PublishPolicyDeclared"); err != nil {
		return err
	}
	return s.EventSink.PublishPolicyDeclared(ctx, d)
}

type failProcessed struct {
	ports.ProcessedEvents
	f *faults
}

func (p *failProcessed) Claim(ctx context.Context, consumer, id string) (bool, error) {
	if err := p.f.hit("ProcessedEvents.Claim"); err != nil {
		return false, err
	}
	return p.ProcessedEvents.Claim(ctx, consumer, id)
}
