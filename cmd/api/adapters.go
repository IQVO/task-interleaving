package main

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	inboundhttp "github.com/claudioed/task-interleaving/internal/adapters/inbound/http"
	"github.com/claudioed/task-interleaving/internal/adapters/outbound/events"
	"github.com/claudioed/task-interleaving/internal/adapters/outbound/memory"
	outboxrelay "github.com/claudioed/task-interleaving/internal/adapters/outbound/outbox"
	"github.com/claudioed/task-interleaving/internal/adapters/outbound/postgres"
	"github.com/claudioed/task-interleaving/internal/application/ports"
	"github.com/claudioed/task-interleaving/internal/application/usecases"
	"github.com/claudioed/task-interleaving/internal/bootretry"
)

// adapters is the set of outbound adapters the composition root wires.
type adapters struct {
	policies ports.PolicyRepo
	trips    ports.StationTripRepo
	pool     ports.TaskPool
	book     ports.LocationBook
	events   ports.EventSink
	uow      ports.UnitOfWork
	clock    ports.Clock
	ids      ports.IDGenerator
	// processed is the consumers' processed-event claim table.
	processed ports.ProcessedEvents

	// outbox is the relay's view of the outbox table; nil in the in-memory
	// development mode, which has no relay.
	outbox outboxrelay.Store
	// idempotency is the Idempotency-Key middleware of the POST; nil in the
	// in-memory development mode, which does not enforce the header.
	idempotency func(http.Handler) http.Handler
}

// buildAdapters wires Postgres when databaseURL is set (running the embedded
// migrations first) or in-memory adapters otherwise. The returned func closes
// what was opened.
func buildAdapters(ctx context.Context, databaseURL, migrationsURL string, logger *slog.Logger) (adapters, func(), error) {
	noop := func() {}
	if databaseURL == "" {
		logger.Info("DATABASE_URL not configured; using in-memory adapters (no outbox relay, Idempotency-Key not enforced)")
		return memoryAdapters(), noop, nil
	}

	// The first outbound dial of an injected pod can be reset (Istio native
	// sidecars), so migrations and the first ping retry with backoff; on
	// exhaustion the LAST error is returned and the process refuses to boot.
	if err := bootretry.Retry(ctx, logger, "run migrations", func() error {
		return postgres.RunMigrations(migrationsURL)
	}); err != nil {
		return adapters{}, noop, err
	}
	pool, err := postgres.NewPool(ctx, databaseURL)
	if err != nil {
		return adapters{}, noop, err
	}
	if err := bootretry.Retry(ctx, logger, "ping postgres", func() error { return pool.Ping(ctx) }); err != nil {
		pool.Close()
		return adapters{}, noop, err
	}
	logger.Info("postgres adapters configured")
	return postgresAdapters(pool), pool.Close, nil
}

func memoryAdapters() adapters {
	policies, trips := memory.NewPolicyRepo(), memory.NewStationTripRepo()
	pool, book := memory.NewTaskPool(), memory.NewLocationBook()
	log, processed := memory.NewEventLog(), memory.NewProcessedEventRepo()
	return adapters{
		policies: policies, trips: trips, pool: pool, book: book, events: log, processed: processed,
		uow:   memory.NewUnitOfWork(policies, trips, pool, book, log, processed),
		clock: memory.SystemClock{}, ids: memory.RandomIDs{},
	}
}

func postgresAdapters(pgPool *pgxpool.Pool) adapters {
	ob := postgres.NewOutboxRepo(pgPool)
	ad := adapters{
		policies: postgres.NewPolicyRepo(pgPool), trips: postgres.NewStationTripRepo(pgPool),
		pool: postgres.NewTaskPool(pgPool), book: postgres.NewLocationBook(pgPool),
		events: events.NewSink(ob), uow: postgres.NewUnitOfWork(pgPool),
		clock: memory.SystemClock{}, ids: memory.RandomIDs{},
		processed: postgres.NewProcessedEventRepo(pgPool),
		outbox:    ob,
	}
	ad.idempotency = inboundhttp.NewIdempotency(postgres.NewIdempotencyStore(pgPool), ad.uow, ad.clock).Middleware
	return ad
}

// intake is the claim-and-apply skeleton of every consumer use case.
func (a adapters) intake() usecases.Intake {
	return usecases.Intake{UoW: a.uow, Processed: a.processed}
}

// ranker is the pure recommender's view of the local copies, shared by the
// read-only simulation and by the hint issuing use cases.
func (a adapters) ranker() usecases.Ranker {
	return usecases.Ranker{Pool: a.pool, Book: a.book}
}

// tripWriter is what every use case that changes a StationTrip saves through.
func (a adapters) tripWriter() usecases.TripWriter {
	return usecases.TripWriter{Trips: a.trips, Events: a.events}
}

// expireHints is the sweeper the expiry worker drives.
func (a adapters) expireHints() *usecases.ExpireHints {
	return &usecases.ExpireHints{TripWriter: a.tripWriter(), UoW: a.uow, Clock: a.clock}
}

// buildServer wires the use cases into the HTTP adapter.
func buildServer(a adapters, readiness *inboundhttp.Readiness) *inboundhttp.Server {
	return &inboundhttp.Server{
		DeclarePolicy: &usecases.DeclarePolicy{Policies: a.policies, Events: a.events, UoW: a.uow, Clock: a.clock},
		GetPolicy:     &usecases.GetPolicy{Policies: a.policies},
		ListHints:     &usecases.ListHints{Trips: a.trips},
		GetHint:       &usecases.GetHint{Trips: a.trips},
		GetStation:    &usecases.GetStation{Trips: a.trips},
		Recommend:     &usecases.Recommend{Trips: a.trips, Policies: a.policies, Ranker: a.ranker()},
		Idempotency:   a.idempotency,
		Readiness:     readiness,
	}
}

// migrationsDatabaseURL returns MIGRATIONS_DATABASE_URL when set (a direct
// DSN: golang-migrate's advisory lock does not survive PgBouncer transaction
// pooling), else databaseURL.
func migrationsDatabaseURL(databaseURL string) string {
	return getenv(postgres.EnvMigrationsDatabaseURL, databaseURL)
}
