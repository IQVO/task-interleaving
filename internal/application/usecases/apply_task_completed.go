package usecases

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/claudioed/task-interleaving/internal/application/ports"
	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/domain/policy"
	"github.com/claudioed/task-interleaving/internal/domain/recommend"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
	"github.com/claudioed/task-interleaving/internal/domain/travel"
)

// TaskCompletion is a decoded TaskCompleted payload: only the task id is
// required, the end position is read from the local copy of the task. At is the
// completion instant (the CloudEvents time); zero means "now".
type TaskCompletion struct {
	TaskID string
	At     time.Time
}

// ApplyTaskCompleted marks the task COMPLETED and records the completion on the
// claimer's trip: the station now stands at the task's to_location. When the
// completion is newer than the recorded one and the site declared a policy, it
// runs the recommender over the AVAILABLE pool and issues a hint if candidates
// exist. A site without a policy is inert: the position is recorded, no hint is
// issued. A task never seen is recorded as completed and otherwise ignored.
type ApplyTaskCompleted struct {
	Intake
	TripWriter
	Policies ports.PolicyRepo
	Pool     ports.TaskPool
	Ranker   Ranker
	Clock    ports.Clock
	IDs      ports.IDGenerator
}

// Handle runs the use case. A payload that can never be applied is wrapped in
// ErrInvalidEvent before anything is claimed.
func (uc *ApplyTaskCompleted) Handle(ctx context.Context, eventID string, c TaskCompletion) (Outcome, error) {
	if blank(c.TaskID) {
		return "", invalid(errors.New("task_id is required"))
	}
	return uc.apply(ctx, FulfillmentConsumer, eventID, func(ctx context.Context) (Outcome, error) {
		task, found, err := uc.Pool.Get(ctx, c.TaskID)
		if err != nil {
			return "", fmt.Errorf("read task: %w", err)
		}
		if found && task.State == repository.TaskCompleted {
			return OutcomeStale, nil
		}
		task.ID, task.State = c.TaskID, repository.TaskCompleted
		if err := uc.Pool.Save(ctx, task); err != nil {
			return "", fmt.Errorf("save task: %w", err)
		}
		station, err := stationtrip.NewStationID(task.ClaimedBy)
		if err != nil || blank(task.ToLocation) {
			return OutcomeIgnored, nil
		}
		return uc.record(ctx, station, task, c.At)
	})
}

// record puts the station at the end of the completed task and issues the hint.
func (uc *ApplyTaskCompleted) record(ctx context.Context, station stationtrip.StationID, task repository.Task, at time.Time) (Outcome, error) {
	place, bookSite, err := uc.Ranker.Origin(ctx, task.ToLocation, nil)
	if err != nil {
		return "", err
	}
	site := task.SiteID
	if site == "" {
		site = bookSite
	}
	if _, err := policy.NewSiteID(site); err != nil {
		return OutcomeIgnored, nil
	}
	if at.IsZero() {
		at = uc.Clock.Now()
	}
	trip, loaded, err := uc.load(ctx, station)
	if err != nil {
		return "", err
	}
	pos := stationtrip.Position{Site: policy.SiteID(site), Place: place}
	changed, err := trip.RecordCompletion(recommend.TaskID(task.ID), at, pos)
	if err != nil {
		return "", invalid(err)
	}
	if !changed {
		return OutcomeStale, nil
	}
	if err := uc.issueHint(ctx, trip, place, recommend.TaskID(task.ID)); err != nil {
		return "", err
	}
	if err := uc.persist(ctx, trip, loaded); err != nil {
		return "", err
	}
	return OutcomeApplied, nil
}

// issueHint issues a hint for the trip's new position when the site declared a
// policy, its origin has geometry and at least one candidate qualifies.
func (uc *ApplyTaskCompleted) issueHint(ctx context.Context, trip *stationtrip.StationTrip, origin travel.Place, completed recommend.TaskID) error {
	pos, _ := trip.Position()
	pol, err := uc.Policies.Get(ctx, pos.Site)
	if errors.Is(err, repository.ErrPolicyNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load policy: %w", err)
	}
	cands, err := uc.Ranker.Rank(ctx, pol.Policy, origin, completed)
	if errors.Is(err, recommend.ErrOriginUnknown) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(cands) == 0 {
		return nil
	}
	if err := trip.IssueHint(stationtrip.HintID(uc.IDs.NewHintID()), cands, pol.Policy, uc.Clock.Now()); err != nil {
		return fmt.Errorf("issue hint: %w", err)
	}
	return nil
}
