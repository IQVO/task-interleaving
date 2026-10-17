package kafka

import (
	"context"
	"log/slog"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"

	"github.com/claudioed/task-interleaving/internal/application/usecases"
)

// FulfillmentTopic is fulfillment-execution's integration topic.
const FulfillmentTopic = "warehouse.fulfillment.events"

// The fulfillment-execution types this service acts on, byte-identical to its
// AsyncAPI. TaskCompleted exists on the producer today. TaskAvailable and
// TaskClaimed await producer change P2 (ADR 0003): they are decoded from the
// pinned consumer-first contract and are simply never seen until the producer
// ships them. Every other type on the topic is ignored.
const (
	TypeTaskAvailable = "com.warehouse.wes.fulfillment-execution.task.TaskAvailable"
	TypeTaskClaimed   = "com.warehouse.wes.fulfillment-execution.task.TaskClaimed"
	TypeTaskCompleted = "com.warehouse.wes.fulfillment-execution.task.TaskCompleted"
)

// The payloads mirror the pinned v1 contracts (restated here, never imported).
// A field that is absent decodes to its zero value and the use case decides
// whether the message can ever be applied; the consumer never crashes on a
// missing field. Fields this service does not read (cpt, available_at, the
// producer's work_unit_id, ...) are ignored.

type taskAvailableData struct {
	TaskID               string   `json:"task_id"`
	TaskType             string   `json:"task_type"`
	SiteID               string   `json:"site_id"`
	FromLocation         string   `json:"from_location"`
	ToLocation           string   `json:"to_location"`
	RequiredCapabilities []string `json:"required_capabilities"`
	PriorityBand         string   `json:"priority_band"`
}

type taskClaimedData struct {
	TaskID    string    `json:"task_id"`
	TaskType  string    `json:"task_type"`
	StationID string    `json:"station_id"`
	ClaimedAt time.Time `json:"claimed_at"`
}

type taskCompletedData struct {
	TaskID    string `json:"task_id"`
	StationID string `json:"station_id"`
}

// FulfillmentUseCases are the three use cases the fulfillment consumer
// dispatches to.
type FulfillmentUseCases struct {
	Available *usecases.ApplyTaskAvailable
	Claimed   *usecases.ApplyTaskClaimed
	Completed *usecases.ApplyTaskCompleted
}

// FulfillmentHandlers maps the fulfillment topic's consumed types to their use
// cases.
func FulfillmentHandlers(uc FulfillmentUseCases, logger *slog.Logger) map[string]HandlerFunc {
	logger = defaultLogger(logger)
	return map[string]HandlerFunc{
		TypeTaskAvailable: func(ctx context.Context, e ce.Event) error {
			d, err := decodeData[taskAvailableData](e)
			if err != nil {
				return err
			}
			return apply(ctx, logger, e, func() (usecases.Outcome, error) {
				return uc.Available.Handle(ctx, e.ID(), usecases.TaskAvailability{
					TaskID: d.TaskID, TaskType: d.TaskType, SiteID: d.SiteID,
					FromLocation: d.FromLocation, ToLocation: d.ToLocation,
					RequiredCapabilities: d.RequiredCapabilities, PriorityBand: d.PriorityBand,
				})
			})
		},
		TypeTaskClaimed: func(ctx context.Context, e ce.Event) error {
			d, err := decodeData[taskClaimedData](e)
			if err != nil {
				return err
			}
			return apply(ctx, logger, e, func() (usecases.Outcome, error) {
				return uc.Claimed.Handle(ctx, e.ID(), usecases.TaskClaim{
					TaskID: d.TaskID, TaskType: d.TaskType, StationID: d.StationID, At: firstTime(d.ClaimedAt, e.Time()),
				})
			})
		},
		TypeTaskCompleted: func(ctx context.Context, e ce.Event) error {
			d, err := decodeData[taskCompletedData](e)
			if err != nil {
				return err
			}
			return apply(ctx, logger, e, func() (usecases.Outcome, error) {
				return uc.Completed.Handle(ctx, e.ID(), usecases.TaskCompletion{TaskID: d.TaskID, StationID: d.StationID, At: e.Time()})
			})
		},
	}
}

// firstTime returns primary unless it is the zero time.
func firstTime(primary, fallback time.Time) time.Time {
	if primary.IsZero() {
		return fallback
	}
	return primary
}

// NewFulfillmentConsumer reads fulfillment-execution's topic under groupID
// (FULFILLMENT_CONSUMER_GROUP, never a literal).
func NewFulfillmentConsumer(brokers []string, groupID string, uc FulfillmentUseCases, logger *slog.Logger) *Consumer {
	return NewFulfillmentConsumerForTopic(brokers, FulfillmentTopic, groupID, uc, logger)
}

// NewFulfillmentConsumerForTopic is NewFulfillmentConsumer on an explicit topic.
func NewFulfillmentConsumerForTopic(brokers []string, topic, groupID string, uc FulfillmentUseCases, logger *slog.Logger) *Consumer {
	return newConsumer("fulfillment consumer", brokers, topic, groupID, FulfillmentHandlers(uc, logger), logger)
}
