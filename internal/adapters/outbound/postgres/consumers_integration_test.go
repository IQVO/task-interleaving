//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/claudioed/task-interleaving/internal/application/ports"
	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/application/usecases"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
	"github.com/claudioed/task-interleaving/internal/testing/repocontract"
)

// world is a site with a declared policy, two locations 7 m apart, a task the
// station STN-1 is about to complete and an open task near its end.
type world struct {
	s      stores
	clock  *testClock
	ids    *seqIDs
	ranker usecases.Ranker
	sink   *recordingSink
}

func check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func seedWorld(t *testing.T) world {
	t.Helper()
	s := newStores(t)
	ctx := context.Background()
	w := world{
		s: s, clock: &testClock{now: repocontract.T0.Add(time.Hour)}, ids: &seqIDs{},
		ranker: usecases.Ranker{Pool: s.tasks, Book: s.book}, sink: &recordingSink{},
	}
	check(t, s.policies.Save(ctx, repository.PolicyRecord{Policy: repocontract.Policy(t, repocontract.Site), DeclaredAt: repocontract.T0}, 0))
	check(t, s.book.SaveZone(ctx, repository.Zone{ID: "z1", SiteCode: "SITE-1", ZoneCode: "FWD"}))
	for _, loc := range []string{"LOC-T", "LOC-F"} {
		check(t, s.book.SaveLocation(ctx, loc, "z1"))
	}
	check(t, s.book.SaveGeometry(ctx, "LOC-T", 0, 0, 0))
	check(t, s.book.SaveGeometry(ctx, "LOC-F", 3, 4, 0))
	w.saveTasks(t,
		repository.Task{ID: "task-done", Type: "PICK", SiteID: "SITE-1", FromLocation: "LOC-T", ToLocation: "LOC-T", State: repository.TaskClaimed, ClaimedBy: "STN-1"},
		repository.Task{ID: "task-next", Type: "PICK", SiteID: "SITE-1", FromLocation: "LOC-F", ToLocation: "LOC-F", State: repository.TaskAvailable},
	)
	return w
}

func (w world) saveTasks(t *testing.T, tasks ...repository.Task) {
	t.Helper()
	for _, tk := range tasks {
		check(t, w.s.tasks.Save(context.Background(), tk))
	}
}

func (w world) intake() usecases.Intake {
	return usecases.Intake{UoW: w.s.uow, Processed: w.s.processed}
}

func (w world) writer(sink ports.EventSink) usecases.TripWriter {
	return usecases.TripWriter{Trips: w.s.trips, Events: sink}
}

func (w world) completer(sink ports.EventSink) *usecases.ApplyTaskCompleted {
	return &usecases.ApplyTaskCompleted{
		Intake: w.intake(), TripWriter: w.writer(sink),
		Policies: w.s.policies, Pool: w.s.tasks, Ranker: w.ranker, Clock: w.clock, IDs: w.ids,
	}
}

func (w world) complete(t *testing.T, eventID, task string) {
	t.Helper()
	out, err := w.completer(w.sink).Handle(context.Background(), eventID, usecases.TaskCompletion{TaskID: task, At: w.clock.now})
	if err != nil || out != usecases.OutcomeApplied {
		t.Fatalf("completion of %s = %v %v", task, out, err)
	}
}

func (w world) hint(t *testing.T, id string) repository.HintRecord {
	t.Helper()
	rec, err := w.s.trips.GetHint(context.Background(), stationtrip.HintID(id))
	if err != nil {
		t.Fatalf("hint %s: %v", id, err)
	}
	return rec
}

// assertRolledBack: the completion, the trip, the hint and the claim are gone.
func assertRolledBack(t *testing.T, w world) {
	t.Helper()
	task, _, _ := w.s.tasks.Get(context.Background(), "task-done")
	if task.State != repository.TaskClaimed {
		t.Fatalf("the completion must have rolled back, the task is %s", task.State)
	}
	for _, table := range []string{"station_trips", "hints", "candidates", "processed_events"} {
		if n := w.s.count(table); n != 0 {
			t.Fatalf("%s has %d rows after the rollback", table, n)
		}
	}
}

// TestConsumerEffectAndClaimRollBackTogether runs the real ApplyTaskCompleted
// use case over Postgres: when the outbox write after the effect fails, neither
// the task's completion, the trip, the hint nor the processed-event claim
// survives, so a redelivery of the same CloudEvents id is processed (and then
// deduplicated).
func TestConsumerEffectAndClaimRollBackTogether(t *testing.T) {
	w := seedWorld(t)
	ctx := context.Background()
	done := usecases.TaskCompletion{TaskID: "task-done", At: w.clock.now}
	if _, err := w.completer(failingSink{}).Handle(ctx, "evt-1", done); !errors.Is(err, errBoom) {
		t.Fatalf("Handle = %v, want the sink failure", err)
	}
	assertRolledBack(t, w)

	for i, want := range []usecases.Outcome{usecases.OutcomeApplied, usecases.OutcomeDuplicate} {
		out, err := w.completer(w.sink).Handle(ctx, "evt-1", done)
		if err != nil || out != want {
			t.Fatalf("delivery %d = %v %v, want %v", i+1, out, err, want)
		}
	}
	if names := w.sink.recorded(); len(names) != 1 || names[0] != "HintIssued" {
		t.Fatalf("events = %v, want one HintIssued", names)
	}
	if w.s.count("processed_events") != 1 || w.s.count("hints WHERE state = 'Active'") != 1 {
		t.Fatalf("claims %d, active hints %d, want 1 and 1", w.s.count("processed_events"), w.s.count("hints WHERE state = 'Active'"))
	}
	rec := w.hint(t, "hint-2") // hint-1 was minted by the attempt that rolled back
	if len(rec.Hint.Candidates) != 1 || rec.Hint.Candidates[0].TaskID != "task-next" || rec.Hint.Candidates[0].DetourM != 7 {
		t.Fatalf("hint = %+v", rec)
	}
	if _, err := w.s.trips.GetHint(ctx, "hint-1"); !errors.Is(err, repository.ErrHintNotFound) {
		t.Fatalf("the hint of the rolled back attempt survived: %v", err)
	}
}

func (w world) claim(t *testing.T, eventID, task string) {
	t.Helper()
	uc := &usecases.ApplyTaskClaimed{Intake: w.intake(), TripWriter: w.writer(w.sink), Pool: w.s.tasks, Clock: w.clock}
	in := usecases.TaskClaim{TaskID: task, TaskType: "PICK", StationID: "STN-1", At: w.clock.now.Add(10 * time.Second)}
	if out, err := uc.Handle(context.Background(), eventID, in); err != nil || out != usecases.OutcomeApplied {
		t.Fatalf("claim of %s = %v %v", task, out, err)
	}
}

func (w world) sweep(t *testing.T, want int) {
	t.Helper()
	uc := &usecases.ExpireHints{TripWriter: w.writer(w.sink), UoW: w.s.uow, Clock: w.clock}
	if n, err := uc.Handle(context.Background()); err != nil || n != want {
		t.Fatalf("sweep = %d %v, want %d", n, err, want)
	}
}

// TestHintLifecycleOverPostgres drives the whole lifecycle through the use
// cases: a completion issues a hint, a claim of a candidate consumes it, a
// later hint is left to expire and the sweeper expires it.
func TestHintLifecycleOverPostgres(t *testing.T) {
	w := seedWorld(t)
	w.complete(t, "evt-1", "task-done")
	w.claim(t, "evt-2", "task-next")
	consumed := w.hint(t, "hint-1")
	if consumed.Hint.State != stationtrip.StateConsumed || consumed.Hint.ConsumedTask != "task-next" {
		t.Fatalf("hint-1 = %+v", consumed)
	}

	// a second task to do, another one open nearby and a second hint, left to expire
	w.saveTasks(t,
		repository.Task{ID: "task-two", Type: "PICK", SiteID: "SITE-1", FromLocation: "LOC-T", ToLocation: "LOC-T", State: repository.TaskClaimed, ClaimedBy: "STN-1"},
		repository.Task{ID: "task-three", Type: "PICK", SiteID: "SITE-1", FromLocation: "LOC-F", ToLocation: "LOC-F", State: repository.TaskAvailable},
	)
	w.clock.now = w.clock.now.Add(time.Minute)
	w.complete(t, "evt-3", "task-two")
	w.sweep(t, 0)
	w.clock.now = w.clock.now.Add(5 * time.Minute)
	w.sweep(t, 1)
	if expired := w.hint(t, "hint-2"); expired.Hint.State != stationtrip.StateExpired {
		t.Fatalf("hint-2 = %+v", expired)
	}
	if got := fmt.Sprint(w.sink.recorded()); got != "[HintIssued HintConsumed HintIssued HintExpired]" {
		t.Fatalf("events = %v", got)
	}
	w.sweep(t, 0)
}
