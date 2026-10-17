package memory

import "github.com/claudioed/task-interleaving/internal/application/ports"

// Compile-time proof that every in-memory adapter implements its port.
var (
	_ ports.PolicyRepo       = (*PolicyRepo)(nil)
	_ ports.StationTripRepo  = (*StationTripRepo)(nil)
	_ ports.TaskPool         = (*TaskPool)(nil)
	_ ports.LocationBook     = (*LocationBook)(nil)
	_ ports.EventSink        = (*EventLog)(nil)
	_ ports.ProcessedEvents  = (*ProcessedEventRepo)(nil)
	_ ports.IdempotencyStore = (*IdempotencyStore)(nil)
	_ ports.UnitOfWork       = (*UnitOfWork)(nil)
	_ ports.Clock            = SystemClock{}
	_ ports.IDGenerator      = RandomIDs{}
	_ Snapshotter            = (*PolicyRepo)(nil)
	_ Snapshotter            = (*StationTripRepo)(nil)
	_ Snapshotter            = (*TaskPool)(nil)
	_ Snapshotter            = (*LocationBook)(nil)
	_ Snapshotter            = (*EventLog)(nil)
	_ Snapshotter            = (*ProcessedEventRepo)(nil)
)
