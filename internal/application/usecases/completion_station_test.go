package usecases_test

import (
	"errors"
	"testing"

	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/application/usecases"
)

// ADR 0003: TaskCompleted names the station that completed the task. A task
// whose TaskClaimed never arrived (producer change P2 is pending) has no
// claimer in the pool copy, and the completion still lands on the named station.
func TestACompletionNamesItsStationWhenNobodyClaimedTheTask(t *testing.T) {
	e := newEnv()
	e.seed(t)
	mustNil(t, e.pool.Save(bg, repository.Task{
		ID: "T-open", Type: "PICK", SiteID: "S1", FromLocation: "L0", ToLocation: "L0", State: repository.TaskAvailable,
	}))
	out, err := e.completed().Handle(bg, "evt-1", usecases.TaskCompletion{TaskID: "T-open", StationID: "STN-9"})
	if err != nil || out != usecases.OutcomeApplied {
		t.Fatalf("Handle = %v, %v", out, err)
	}
	if _, err := e.trips.Get(bg, "STN-9"); err != nil {
		t.Fatalf("the named station must have the completion: %v", err)
	}
}

// The station of the event wins over the claimer of the pool copy.
func TestTheStationOfTheEventWinsOverTheClaimer(t *testing.T) {
	e := newEnv()
	e.seed(t)
	out, err := e.completed().Handle(bg, "evt-1", usecases.TaskCompletion{TaskID: "T0", StationID: "STN-2"})
	if err != nil || out != usecases.OutcomeApplied {
		t.Fatalf("Handle = %v, %v", out, err)
	}
	if _, err := e.trips.Get(bg, "STN-2"); err != nil {
		t.Fatalf("the event's station must be recorded: %v", err)
	}
	if _, err := e.trips.Get(bg, "STN-1"); !errors.Is(err, repository.ErrStationNotFound) {
		t.Fatalf("the claimer must not be recorded when the event names another station: %v", err)
	}
}

// Without station_id the claimer is used, as before.
func TestABlankStationFallsBackToTheClaimer(t *testing.T) {
	e := newEnv()
	e.seed(t)
	out, err := e.completed().Handle(bg, "evt-1", usecases.TaskCompletion{TaskID: "T0", StationID: "  "})
	if err != nil || out != usecases.OutcomeApplied {
		t.Fatalf("Handle = %v, %v", out, err)
	}
	if _, err := e.trips.Get(bg, "STN-1"); err != nil {
		t.Fatalf("the claimer must be recorded: %v", err)
	}
}
