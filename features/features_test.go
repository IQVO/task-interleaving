// Package features_test hosts the godog (Cucumber for Go) acceptance suite. It
// drives the REAL use cases (the application layer) over the in-memory
// adapters, wired the way cmd/api wires them without DATABASE_URL, and feeds
// the event-fed local copies (task pool, claims, location geometry) through the
// consumer use cases with the payloads the producers' contracts define (ADR
// 0003). Later steps of the service put the HTTP router and the Kafka consumers
// in front of the same world.
package features_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"github.com/claudioed/task-interleaving/internal/adapters/outbound/memory"
	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/application/usecases"
)

// TestFeatures runs every Gherkin feature of this directory (godog Paths are
// relative to the package directory, so "." is features/).
func TestFeatures(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: InitializeScenario,
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"."},
			Strict:   true,
			TestingT: t,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("non-zero status returned, failed to run feature tests")
	}
}

// bddStart is the service clock at the start of every scenario.
var bddStart = time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)

// bddClock is the settable service clock: scenarios say how much time passes.
type bddClock struct{ now time.Time }

func (c *bddClock) Now() time.Time { return c.now }

// sequentialIDs mints hint-0001, hint-0002, ... so scenarios can name hints by
// number.
type sequentialIDs struct{ n int }

func (s *sequentialIDs) NewHintID() string {
	s.n++
	return hintID(s.n)
}

func hintID(n int) string { return fmt.Sprintf("hint-%04d", n) }

// app is the set of use cases of one scenario.
type app struct {
	declarePolicy  *usecases.DeclarePolicy
	getPolicy      *usecases.GetPolicy
	recommend      *usecases.Recommend
	available      *usecases.ApplyTaskAvailable
	claimed        *usecases.ApplyTaskClaimed
	completed      *usecases.ApplyTaskCompleted
	expire         *usecases.ExpireHints
	zone           *usecases.ApplyZone
	location       *usecases.ApplyLocationRegistered
	decommissioned *usecases.ApplyLocationDecommissioned
	geometry       *usecases.ApplyGeometryUpdated
	listHints      *usecases.ListHints
	getHint        *usecases.GetHint
	getStation     *usecases.GetStation
}

// wire builds the application over fresh in-memory adapters, as cmd/api does.
func wire(clock *bddClock) (app, *memory.EventLog) {
	policies, trips := memory.NewPolicyRepo(), memory.NewStationTripRepo()
	pool, book := memory.NewTaskPool(), memory.NewLocationBook()
	events, processed := memory.NewEventLog(), memory.NewProcessedEventRepo()
	uow := memory.NewUnitOfWork(policies, trips, pool, book, events, processed)
	intake := usecases.Intake{UoW: uow, Processed: processed}
	writer := usecases.TripWriter{Trips: trips, Events: events}
	ranker := usecases.Ranker{Pool: pool, Book: book}
	ids := &sequentialIDs{}
	return app{
		declarePolicy: &usecases.DeclarePolicy{Policies: policies, Events: events, UoW: uow, Clock: clock},
		getPolicy:     &usecases.GetPolicy{Policies: policies},
		recommend:     &usecases.Recommend{Trips: trips, Policies: policies, Ranker: ranker},
		available:     &usecases.ApplyTaskAvailable{Intake: intake, Pool: pool},
		claimed:       &usecases.ApplyTaskClaimed{Intake: intake, TripWriter: writer, Pool: pool, Clock: clock},
		completed: &usecases.ApplyTaskCompleted{
			Intake: intake, TripWriter: writer, Policies: policies, Pool: pool,
			Ranker: ranker, Clock: clock, IDs: ids,
		},
		expire:         &usecases.ExpireHints{TripWriter: writer, UoW: uow, Clock: clock},
		zone:           &usecases.ApplyZone{Intake: intake, Book: book},
		location:       &usecases.ApplyLocationRegistered{Intake: intake, Book: book},
		decommissioned: &usecases.ApplyLocationDecommissioned{Intake: intake, Book: book},
		geometry:       &usecases.ApplyGeometryUpdated{Intake: intake, Book: book},
		listHints:      &usecases.ListHints{Trips: trips},
		getHint:        &usecases.GetHint{Trips: trips},
		getStation:     &usecases.GetStation{Trips: trips},
	}, events
}

// world is the per-scenario state.
type world struct {
	uc    app
	log   *memory.EventLog
	clock *bddClock
	ctx   context.Context

	err        error
	errChecked bool
	unchecked  []error
	outcome    usecases.Outcome

	eventSeq  int
	redeliver func()

	policy  repository.PolicyRecord
	sim     usecases.Recommendation
	expired int
}

func (w *world) start() {
	w.clock = &bddClock{now: bddStart}
	w.uc, w.log = wire(w.clock)
	w.ctx = context.Background()
	w.err, w.errChecked, w.unchecked, w.outcome = nil, false, nil, ""
	w.eventSeq, w.redeliver = 0, nil
	w.policy, w.sim, w.expired = repository.PolicyRecord{}, usecases.Recommendation{}, 0
}

// run starts a command: a failure of the previous one that no step looked at
// is remembered and fails the scenario at the end.
func (w *world) run(fn func() error) {
	if w.err != nil && !w.errChecked {
		w.unchecked = append(w.unchecked, w.err)
	}
	w.err, w.errChecked = fn(), false
}

// deliver hands one consumed event to its use case under a fresh CloudEvents id
// and remembers how to deliver the same event again.
func (w *world) deliver(handle func(id string) (usecases.Outcome, error)) {
	w.eventSeq++
	id := fmt.Sprintf("evt-%d", w.eventSeq)
	w.redeliver = func() {
		w.run(func() error {
			var err error
			w.outcome, err = handle(id)
			return err
		})
	}
	w.redeliver()
}

// finish fails the scenario when a command failed and no step looked at it.
func (w *world) finish() error {
	if w.err != nil && !w.errChecked {
		w.unchecked = append(w.unchecked, w.err)
	}
	if len(w.unchecked) == 0 {
		return nil
	}
	return fmt.Errorf("unchecked command errors: %w", errors.Join(w.unchecked...))
}

// InitializeScenario registers every step.
func InitializeScenario(sc *godog.ScenarioContext) {
	w := &world{}
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		w.start()
		return ctx, nil
	})
	sc.After(func(ctx context.Context, _ *godog.Scenario, err error) (context.Context, error) {
		if err != nil {
			return ctx, err
		}
		return ctx, w.finish()
	})
	registerSetupSteps(sc, w)
	registerPolicySteps(sc, w)
	registerFlowSteps(sc, w)
	registerAssertionSteps(sc, w)
}
