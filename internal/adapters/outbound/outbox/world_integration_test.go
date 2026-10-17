//go:build integration

package outbox_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/claudioed/task-interleaving/internal/adapters/outbound/events"
	"github.com/claudioed/task-interleaving/internal/adapters/outbound/postgres"
	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/application/usecases"
	"github.com/claudioed/task-interleaving/internal/domain/policy"
	"github.com/claudioed/task-interleaving/internal/testing/kafkatest"
	"github.com/claudioed/task-interleaving/internal/testing/pgtest"
)

// The ids and values of the golden files of the events adapter (the wire
// contract has ONE source of truth: internal/adapters/outbound/events/testdata).
const (
	stationID  = "STN-07"
	siteID     = "SITE-1"
	hintID     = "hint-3b0e8d2a-7c41-4f6a-9d15-2a8b6c4e1f07"
	idPolicyV1 = "5a4b3c2d-1e0f-4a9b-8c7d-6e5f4a3b2c1d"
	idPolicyV2 = "7e6d5c4b-3a29-4180-9f7e-6d5c4b3a2918"
	idIssued   = "6f1d2c3b-4a5e-4b7c-8d9e-0a1b2c3d4e5f"
	idConsumed = "0b9a8c7d-6e5f-4a3b-9c2d-1e0f9a8b7c6d"
	idExpired  = "4d3c2b1a-0f9e-4d8c-8b7a-6f5e4d3c2b1a"
)

func at(h, m, s int) time.Time { return time.Date(2026, 10, 17, h, m, s, 0, time.UTC) }

// The database itself comes from internal/testing/pgtest, which starts it
// through testcontainers-go/modules/postgres (referenced here so the fleet's
// fitness test sees the import in this package).
var _ = tcpostgres.Run

// clock is a ports.Clock the test moves by hand.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// fixedHints mints the one hint id of the golden files (ports.IDGenerator).
type fixedHints struct{}

func (fixedHints) NewHintID() string { return hintID }

// ids hands out the CloudEvents ids of the golden files, in publication order.
func ids(list ...string) func() string {
	var mu sync.Mutex
	i := 0
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		id := list[i%len(list)]
		i++
		return id
	}
}

// world is the production wiring of the use cases over real Postgres, with the
// production EventSink writing the outbox on explicitly created topics.
type world struct {
	t              *testing.T
	pool           *pgxpool.Pool
	clock          *clock
	eventsTopic    string
	analyticsTopic string
	trips          *postgres.StationTripRepo
	tasks          *postgres.TaskPool
	book           *postgres.LocationBook
	policies       *postgres.PolicyRepo
	processed      *postgres.ProcessedEventRepo
	uow            *postgres.UnitOfWork
	sink           *events.Sink
}

func newWorld(t *testing.T, eventIDs ...string) *world {
	t.Helper()
	pool := pgtest.NewPool(t)
	w := &world{
		t: t, pool: pool, clock: &clock{now: at(10, 0, 0)},
		eventsTopic:    kafkatest.Topic(t, events.TopicEvents),
		analyticsTopic: kafkatest.Topic(t, events.TopicAnalytics),
		trips:          postgres.NewStationTripRepo(pool),
		tasks:          postgres.NewTaskPool(pool),
		book:           postgres.NewLocationBook(pool),
		policies:       postgres.NewPolicyRepo(pool),
		processed:      postgres.NewProcessedEventRepo(pool),
		uow:            postgres.NewUnitOfWork(pool),
	}
	w.sink = events.NewSink(postgres.NewOutboxRepo(pool))
	w.sink.EventsTopic, w.sink.AnalyticsTopic = w.eventsTopic, w.analyticsTopic
	w.sink.NewID = ids(eventIDs...)
	return w
}

func (w *world) check(err error) {
	w.t.Helper()
	if err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) intake() usecases.Intake { return usecases.Intake{UoW: w.uow, Processed: w.processed} }

func (w *world) writer() usecases.TripWriter {
	return usecases.TripWriter{Trips: w.trips, Events: w.sink}
}

// seedLayout puts three locations of SITE-1 on a line: the station ends at 0 m,
// the PICK task starts 4.5 m away and the REPLENISH task 12 m away.
func (w *world) seedLayout() {
	w.t.Helper()
	ctx := context.Background()
	w.check(w.book.SaveZone(ctx, repository.Zone{ID: "z1", SiteCode: siteID, ZoneCode: "FWD"}))
	for code, x := range map[string]float64{"WH1-FWD-A01-01-01-A": 0, "WH1-FWD-A01-02-01-A": 4.5, "WH1-FWD-A02-01-01-A": 12} {
		w.check(w.book.SaveLocation(ctx, code, "z1"))
		w.check(w.book.SaveGeometry(ctx, code, x, 0, 0))
	}
	for _, tk := range []repository.Task{
		{ID: "task-8a1f", Type: "PICK", SiteID: siteID, FromLocation: "WH1-FWD-A01-01-01-A", ToLocation: "WH1-FWD-A01-01-01-A", State: repository.TaskClaimed, ClaimedBy: stationID},
		{ID: "task-9c2e", Type: "PICK", SiteID: siteID, FromLocation: "WH1-FWD-A01-02-01-A", ToLocation: "WH1-FWD-A01-02-01-A", State: repository.TaskAvailable},
		{ID: "task-9d10", Type: "REPLENISH", SiteID: siteID, FromLocation: "WH1-FWD-A02-01-01-A", ToLocation: "WH1-FWD-A02-01-01-A", State: repository.TaskAvailable},
	} {
		w.check(w.tasks.Save(ctx, tk))
	}
}

// seedPolicy stores the policy of SITE-1 directly, without the declaration
// event: for the tests that need a hint and nothing else in the outbox.
func (w *world) seedPolicy() {
	w.t.Helper()
	site, err := policy.NewSiteID(siteID)
	w.check(err)
	p, err := policy.New(site, policy.Settings{
		MaxDetourM: 25, MaxCandidates: 3, HintTTL: 90 * time.Second,
		EligibleTaskTypes: []policy.TaskType{"PICK", "REPLENISH"}, VerticalWeight: 1.5,
	})
	w.check(err)
	w.check(w.policies.Save(context.Background(), repository.PolicyRecord{Policy: p, DeclaredAt: at(11, 0, 0)}, 0))
}

// declarePolicy declares the policy of SITE-1 twice (versions 1 and 2) so the
// hint carries policy_version 2, at 10:00 and 11:00.
func (w *world) declarePolicy() {
	w.t.Helper()
	uc := &usecases.DeclarePolicy{Policies: w.policies, Events: w.sink, UoW: w.uow, Clock: w.clock}
	weight, one := 1.5, int64(1)
	in := usecases.DeclarePolicyInput{
		SiteID: siteID, MaxDetourM: 20, MaxCandidates: 3, HintTTL: 90 * time.Second,
		EligibleTaskTypes: []string{"PICK", "REPLENISH"}, VerticalWeight: &weight,
	}
	_, err := uc.Handle(context.Background(), in)
	w.check(err)
	w.clock.set(at(11, 0, 0))
	in.MaxDetourM, in.IfMatch = 25, &one
	_, err = uc.Handle(context.Background(), in)
	w.check(err)
}

// completeTask delivers TaskCompleted of task-8a1f at 12:00:00: the station
// stands at 0 m, the policy is declared and two open tasks qualify.
func (w *world) completeTask() {
	w.t.Helper()
	w.clock.set(at(12, 0, 0))
	uc := &usecases.ApplyTaskCompleted{
		Intake: w.intake(), TripWriter: w.writer(), Policies: w.policies, Pool: w.tasks,
		Ranker: usecases.Ranker{Pool: w.tasks, Book: w.book}, Clock: w.clock, IDs: fixedHints{},
	}
	out, err := uc.Handle(context.Background(), "evt-completed", usecases.TaskCompletion{TaskID: "task-8a1f", At: at(12, 0, 0)})
	if err != nil || out != usecases.OutcomeApplied {
		w.t.Fatalf("ApplyTaskCompleted = %v, %v", out, err)
	}
}

// claimCandidate delivers TaskClaimed of the rank-1 candidate at 12:00:20.
func (w *world) claimCandidate() {
	w.t.Helper()
	uc := &usecases.ApplyTaskClaimed{Intake: w.intake(), TripWriter: w.writer(), Pool: w.tasks, Clock: w.clock}
	in := usecases.TaskClaim{TaskID: "task-9c2e", TaskType: "PICK", StationID: stationID, At: at(12, 0, 20)}
	out, err := uc.Handle(context.Background(), "evt-claimed", in)
	if err != nil || out != usecases.OutcomeApplied {
		w.t.Fatalf("ApplyTaskClaimed = %v, %v", out, err)
	}
}

// expireHint runs the sweeper at 12:01:31, past the hint's 12:01:30 expiry.
func (w *world) expireHint() {
	w.t.Helper()
	w.clock.set(at(12, 1, 31))
	uc := &usecases.ExpireHints{TripWriter: w.writer(), UoW: w.uow, Clock: w.clock}
	if n, err := uc.Handle(context.Background()); err != nil || n != 1 {
		w.t.Fatalf("ExpireHints = %d, %v; want 1", n, err)
	}
}
