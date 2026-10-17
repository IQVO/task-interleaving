package memory

import (
	"context"
	"slices"
	"sync"

	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
)

// EventLog is the in-memory ports.EventSink: it records, in order, every fact
// handed to the outbox. It stands in for the Postgres outbox in BDD and in the
// development mode without DATABASE_URL (where nothing is relayed).
type EventLog struct {
	mu           sync.Mutex
	names        []string
	trips        []stationtrip.Event
	declarations []repository.PolicyDeclaration
}

// NewEventLog constructs an empty EventLog.
func NewEventLog() *EventLog { return &EventLog{} }

// PublishTripEvents implements ports.EventSink.
func (l *EventLog) PublishTripEvents(_ context.Context, events ...stationtrip.Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range events {
		l.names = append(l.names, e.EventName())
		l.trips = append(l.trips, e)
	}
	return nil
}

// PublishPolicyDeclared implements ports.EventSink.
func (l *EventLog) PublishPolicyDeclared(_ context.Context, d repository.PolicyDeclaration) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.names = append(l.names, "InterleavingPolicyDeclared")
	l.declarations = append(l.declarations, d)
	return nil
}

// Names returns the event names in publication order: a test helper.
func (l *EventLog) Names() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.names)
}

// TripEvents returns the recorded station facts in order: a test helper.
func (l *EventLog) TripEvents() []stationtrip.Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.trips)
}

// Declarations returns the recorded policy declarations in order: a test
// helper.
func (l *EventLog) Declarations() []repository.PolicyDeclaration {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.declarations)
}

// Snapshot implements Snapshotter.
func (l *EventLog) Snapshot() func() {
	l.mu.Lock()
	defer l.mu.Unlock()
	names := slices.Clone(l.names)
	trips := slices.Clone(l.trips)
	declarations := slices.Clone(l.declarations)
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.names, l.trips, l.declarations = names, trips, declarations
	}
}
