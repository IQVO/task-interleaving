package usecases

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/claudioed/task-interleaving/internal/application/ports"
	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/domain/recommend"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
)

// TaskClaim is a decoded TaskClaimed payload. At is the claim instant (the
// payload's claimed_at); zero means "now".
type TaskClaim struct {
	TaskID    string
	TaskType  string
	StationID string
	At        time.Time
}

// ApplyTaskClaimed marks the task CLAIMED by the station, which removes it from
// the candidates, and lets the station's trip consume its Active hint when the
// task is one of its candidates. A task never seen is recorded minimally. A
// claim of a non-candidate leaves the hint Active.
type ApplyTaskClaimed struct {
	Intake
	TripWriter
	Pool  ports.TaskPool
	Clock ports.Clock
}

// Handle runs the use case. A payload that can never be applied is wrapped in
// ErrInvalidEvent before anything is claimed.
func (uc *ApplyTaskClaimed) Handle(ctx context.Context, eventID string, c TaskClaim) (Outcome, error) {
	station, err := validateClaim(c)
	if err != nil {
		return "", invalid(err)
	}
	return uc.apply(ctx, FulfillmentConsumer, eventID, func(ctx context.Context) (Outcome, error) {
		task, found, err := uc.Pool.Get(ctx, c.TaskID)
		if err != nil {
			return "", fmt.Errorf("read task: %w", err)
		}
		if found && task.State == repository.TaskCompleted {
			return OutcomeStale, nil
		}
		if !found {
			task = repository.Task{ID: c.TaskID, Type: c.TaskType}
		}
		task.State, task.ClaimedBy = repository.TaskClaimed, string(station)
		if err := uc.Pool.Save(ctx, task); err != nil {
			return "", fmt.Errorf("save task: %w", err)
		}
		return OutcomeApplied, uc.consume(ctx, station, c)
	})
}

// consume lets the claimer's trip consume its hint. A station without a trip
// has no hint, so there is nothing to do.
func (uc *ApplyTaskClaimed) consume(ctx context.Context, station stationtrip.StationID, c TaskClaim) error {
	trip, err := uc.Trips.Get(ctx, station)
	if errors.Is(err, repository.ErrStationNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load station: %w", err)
	}
	at := c.At
	if at.IsZero() {
		at = uc.Clock.Now()
	}
	loaded := trip.Version()
	if !trip.RecordClaim(recommend.TaskID(c.TaskID), at) {
		return nil
	}
	return uc.persist(ctx, trip, loaded)
}

func validateClaim(c TaskClaim) (stationtrip.StationID, error) {
	if blank(c.TaskID) {
		return "", errors.New("task_id is required")
	}
	station, err := stationtrip.NewStationID(c.StationID)
	if err != nil {
		return "", errors.New("station_id is required and must be a valid station id")
	}
	return station, nil
}
