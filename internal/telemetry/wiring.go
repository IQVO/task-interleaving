package telemetry

import (
	"context"
	"time"

	"github.com/claudioed/task-interleaving/internal/application/ports"
	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/application/usecases"
	"github.com/claudioed/task-interleaving/internal/domain/policy"
	"github.com/claudioed/task-interleaving/internal/domain/recommend"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
	"github.com/claudioed/task-interleaving/internal/domain/travel"
)

// TimedRanker wraps a usecases.Ranker and records every Rank run's wall time
// on the recommender latency histogram. The composition root wraps the
// NewRanker implementation with it, so the application layer itself stays
// free of telemetry imports (the hexagonal dependency rule).
func TimedRanker(next usecases.Ranker) usecases.Ranker { return timedRanker{next: next} }

// timedRanker is the TimedRanker wrapper.
type timedRanker struct{ next usecases.Ranker }

// Origin delegates unchanged; only the ranking is timed.
func (t timedRanker) Origin(ctx context.Context, location string, recorded *travel.Point) (travel.Place, string, error) {
	return t.next.Origin(ctx, location, recorded)
}

// Rank delegates and records the run's wall time, successful or not.
func (t timedRanker) Rank(ctx context.Context, pol policy.Policy, origin travel.Place, completed recommend.TaskID) ([]recommend.Candidate, error) {
	start := time.Now()
	defer func() { ObserveRecommender(time.Since(start)) }()
	return t.next.Rank(ctx, pol, origin, completed)
}

// CountingEventSink wraps the outbox EventSink port and counts every hint
// fact that reaches the outbox -- issued, consumed and expired transitions,
// by site -- before delegating. Wired by the composition root, it sees
// exactly the facts a unit of work enqueued.
type CountingEventSink struct{ Next ports.EventSink }

// PublishTripEvents counts the hint facts and delegates.
func (c CountingEventSink) PublishTripEvents(ctx context.Context, events ...stationtrip.Event) error {
	for _, e := range events {
		switch e.(type) {
		case stationtrip.HintIssued:
			HintIssued(ctx, string(e.SiteID()))
		case stationtrip.HintConsumed:
			HintConsumed(ctx, string(e.SiteID()))
		case stationtrip.HintExpired:
			HintExpired(ctx, string(e.SiteID()))
		}
	}
	return c.Next.PublishTripEvents(ctx, events...)
}

// PublishPolicyDeclared delegates unchanged.
func (c CountingEventSink) PublishPolicyDeclared(ctx context.Context, d repository.PolicyDeclaration) error {
	return c.Next.PublishPolicyDeclared(ctx, d)
}
