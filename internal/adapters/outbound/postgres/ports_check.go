package postgres

import "github.com/claudioed/task-interleaving/internal/application/ports"

// Compile-time proof that every outbound port with durable state has a
// Postgres implementation. The EventSink is the transactional outbox's write
// side; it arrives with the events adapter.
var (
	_ ports.PolicyRepo       = (*PolicyRepo)(nil)
	_ ports.StationTripRepo  = (*StationTripRepo)(nil)
	_ ports.TaskPool         = (*TaskPool)(nil)
	_ ports.LocationBook     = (*LocationBook)(nil)
	_ ports.ProcessedEvents  = (*ProcessedEventRepo)(nil)
	_ ports.IdempotencyStore = (*IdempotencyStore)(nil)
	_ ports.UnitOfWork       = (*UnitOfWork)(nil)
)
