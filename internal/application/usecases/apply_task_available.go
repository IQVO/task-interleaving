package usecases

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/claudioed/task-interleaving/internal/application/ports"
	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/domain/policy"
)

// TaskAvailability is a decoded TaskAvailable payload (the pinned consumer-first
// contract of ADR 0003): a task entered, or re-entered, the pool.
type TaskAvailability struct {
	TaskID               string
	TaskType             string
	SiteID               string
	FromLocation         string
	ToLocation           string
	RequiredCapabilities []string
	PriorityBand         string
}

// ApplyTaskAvailable upserts a task into the pool as AVAILABLE, also on
// re-entry after a lease expiry (the claimer is forgotten). A task already
// COMPLETED never re-enters the pool.
type ApplyTaskAvailable struct {
	Intake
	Pool ports.TaskPool
}

// Handle runs the use case. A payload that can never be applied is wrapped in
// ErrInvalidEvent before anything is claimed.
func (uc *ApplyTaskAvailable) Handle(ctx context.Context, eventID string, c TaskAvailability) (Outcome, error) {
	if err := validateAvailability(c); err != nil {
		return "", invalid(err)
	}
	return uc.apply(ctx, FulfillmentConsumer, eventID, func(ctx context.Context) (Outcome, error) {
		cur, found, err := uc.Pool.Get(ctx, c.TaskID)
		if err != nil {
			return "", fmt.Errorf("read task: %w", err)
		}
		if found && cur.State == repository.TaskCompleted {
			return OutcomeStale, nil
		}
		err = uc.Pool.Save(ctx, repository.Task{
			ID:                   c.TaskID,
			Type:                 c.TaskType,
			SiteID:               c.SiteID,
			FromLocation:         c.FromLocation,
			ToLocation:           c.ToLocation,
			RequiredCapabilities: c.RequiredCapabilities,
			PriorityBand:         c.PriorityBand,
			State:                repository.TaskAvailable,
		})
		if err != nil {
			return "", fmt.Errorf("save task: %w", err)
		}
		return OutcomeApplied, nil
	})
}

func validateAvailability(c TaskAvailability) error {
	if blank(c.TaskID) || blank(c.TaskType) {
		return errors.New("task_id and task_type are required")
	}
	if _, err := policy.NewSiteID(c.SiteID); err != nil {
		return errors.New("site_id is required and must be a valid site id")
	}
	if blank(c.FromLocation) {
		return errors.New("from_location is required")
	}
	return nil
}

func blank(s string) bool { return strings.TrimSpace(s) == "" }
