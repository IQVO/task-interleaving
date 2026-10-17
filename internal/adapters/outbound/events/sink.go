package events

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/claudioed/task-interleaving/internal/application/outbox"
	"github.com/claudioed/task-interleaving/internal/application/ports"
	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
)

// Inserter is the write side of the transactional outbox the Sink needs;
// postgres.OutboxRepo implements it (cmd/ wires them, adapters never import
// each other). Called with the ctx handed to UnitOfWork.Do it joins that
// transaction.
type Inserter interface {
	Insert(ctx context.Context, msgs ...outbox.Message) error
}

// Sink implements ports.EventSink: each fact becomes one outbox message per
// destination topic (integration + analytics for InterleavingHintIssued,
// analytics only for the other hint events and the policy declaration), all
// handed to the outbox in ONE Insert so they land atomically with the
// aggregate.
type Sink struct {
	// Outbox receives the encoded messages.
	Outbox Inserter
	// NewID mints a CloudEvents id; uuid.NewString when nil.
	NewID func() string
	// EventsTopic overrides the integration topic (TopicEvents when empty).
	EventsTopic string
	// AnalyticsTopic overrides the analytics topic (TopicAnalytics when empty).
	AnalyticsTopic string
}

var _ ports.EventSink = (*Sink)(nil)

// NewSink returns a Sink writing to out and minting random UUID v4 ids.
func NewSink(out Inserter) *Sink { return &Sink{Outbox: out, NewID: uuid.NewString} }

func (s *Sink) eventsTopic() string {
	if s.EventsTopic == "" {
		return TopicEvents
	}
	return s.EventsTopic
}

func (s *Sink) analyticsTopic() string {
	if s.AnalyticsTopic == "" {
		return TopicAnalytics
	}
	return s.AnalyticsTopic
}

func (s *Sink) mintID() string {
	if s.NewID == nil {
		return uuid.NewString()
	}
	return s.NewID()
}

// PublishTripEvents implements ports.EventSink. An event type the service does
// not publish fails the whole call before anything is written.
func (s *Sink) PublishTripEvents(ctx context.Context, evs ...stationtrip.Event) error {
	var msgs []outbox.Message
	for _, ev := range evs {
		f, err := tripFact(ev)
		if err != nil {
			return err
		}
		built, err := s.build(f, s.mintID())
		if err != nil {
			return err
		}
		msgs = append(msgs, built...)
	}
	return s.insert(ctx, msgs)
}

// PublishPolicyDeclared implements ports.EventSink: the analytics-only
// InterleavingPolicyDeclared fact, written with the analytics topic only.
func (s *Sink) PublishPolicyDeclared(ctx context.Context, d repository.PolicyDeclaration) error {
	msgs, err := s.build(policyFact(d), s.mintID())
	if err != nil {
		return err
	}
	return s.insert(ctx, msgs)
}

func (s *Sink) insert(ctx context.Context, msgs []outbox.Message) error {
	if err := s.Outbox.Insert(ctx, msgs...); err != nil {
		return fmt.Errorf("events: write to the outbox: %w", err)
	}
	return nil
}
