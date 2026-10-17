package usecases

import (
	"context"
	"errors"
	"fmt"

	"github.com/claudioed/task-interleaving/internal/application/ports"
)

// Consumer names namespace the claims of the two consumers in
// ports.ProcessedEvents. They are not Kafka consumer group ids.
const (
	FulfillmentConsumer = "task-interleaving-fulfillment"
	LayoutConsumer      = "task-interleaving-layout"
)

// Outcome reports what a consumer use case did.
type Outcome string

// The outcomes.
const (
	// OutcomeApplied: the copy was updated (or the trip changed).
	OutcomeApplied Outcome = "applied"
	// OutcomeDuplicate: this CloudEvents id was already processed.
	OutcomeDuplicate Outcome = "duplicate"
	// OutcomeStale: the message is older than what the copy already holds
	// (a task already completed, a completion not newer than the recorded one).
	OutcomeStale Outcome = "stale"
	// OutcomeIgnored: the message cannot be applied for lack of local data
	// (a completion of an unknown task, say); nothing but the task changed.
	OutcomeIgnored Outcome = "ignored"
)

// invalid marks a payload that can never be applied; it wraps the cause.
func invalid(err error) error { return fmt.Errorf("%w: %w", ErrInvalidEvent, err) }

// Intake is the claim-and-apply skeleton every consumer use case shares: the
// claim of the CloudEvents id and the effect run in ONE unit of work, so a
// rollback un-claims the id and a redelivery is processed, not skipped.
type Intake struct {
	UoW       ports.UnitOfWork
	Processed ports.ProcessedEvents
}

func (in Intake) apply(ctx context.Context, consumer, eventID string, effect func(ctx context.Context) (Outcome, error)) (Outcome, error) {
	if eventID == "" {
		return "", invalid(errors.New("event id is required"))
	}
	var outcome Outcome
	err := in.UoW.Do(ctx, func(ctx context.Context) error {
		claimed, err := in.Processed.Claim(ctx, consumer, eventID)
		if err != nil {
			return fmt.Errorf("claim processed event: %w", err)
		}
		if !claimed {
			outcome = OutcomeDuplicate
			return nil
		}
		out, err := effect(ctx)
		outcome = out
		return err
	})
	if err != nil {
		return "", err
	}
	return outcome, nil
}
