package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	inboundhttp "github.com/claudioed/task-interleaving/internal/adapters/inbound/http"
	"github.com/claudioed/task-interleaving/internal/adapters/outbound/events"
	outboxrelay "github.com/claudioed/task-interleaving/internal/adapters/outbound/outbox"
	"github.com/claudioed/task-interleaving/internal/adapters/outbound/postgres"
	"github.com/claudioed/task-interleaving/internal/adapters/outbound/system"
	"github.com/claudioed/task-interleaving/internal/application/ports"
	"github.com/claudioed/task-interleaving/internal/application/usecases"
	"github.com/claudioed/task-interleaving/internal/bootretry"
)

// adapters is the set of outbound adapters the composition root wires. They
// are the real ones: Postgres for every repository and the outbox, the system
// clock and random ids. There is no in-memory variant in the binary
// (internal/architecture's TestRuntimeBinaryHasNoInMemoryAdapters).
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

	// outbox is the relay's view of the outbox table.
	outbox outboxrelay.Store
	// idempotency is the Idempotency-Key middleware of the POST.
	idempotency func(http.Handler) http.Handler
	// ping is the Postgres liveness check behind /readyz.
	ping func(context.Context) error
}

// errNoDatabase is the boot error of a missing DATABASE_URL.
var errNoDatabase = errors.New("DATABASE_URL is required: this context runs on real Postgres")

// buildAdapters runs the embedded migrations against migrationsURL and wires
// the Postgres adapters over databaseURL. The returned func closes what was
// opened. An empty databaseURL is a boot error: there is no fallback runtime.
func buildAdapters(ctx context.Context, databaseURL, migrationsURL string, logger *slog.Logger) (adapters, func(), error) {
	noop := func() {}
	if databaseURL == "" {
		return adapters{}, noop, errNoDatabase
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

func postgresAdapters(pgPool *pgxpool.Pool) adapters {
	ob := postgres.NewOutboxRepo(pgPool)
	ad := adapters{
		policies: postgres.NewPolicyRepo(pgPool), trips: postgres.NewStationTripRepo(pgPool),
		pool: postgres.NewTaskPool(pgPool), book: postgres.NewLocationBook(pgPool),
		events: events.NewSink(ob), uow: postgres.NewUnitOfWork(pgPool),
		clock: system.Clock{}, ids: system.RandomIDs{},
		processed: postgres.NewProcessedEventRepo(pgPool),
		outbox:    ob,
		ping:      pgPool.Ping,
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

// buildReadiness makes /readyz real: the drain gate AND a Postgres ping AND a
// Kafka metadata check (brokers reachable, every runtime topic has a leader).
// The verdict is cached for inboundhttp.DefaultReadinessTTL.
func buildReadiness(a adapters, kafkaCheck func(context.Context) error) *inboundhttp.Readiness {
	return inboundhttp.NewReadiness(0, 0,
		inboundhttp.Dependency{Name: "postgres", Check: a.ping},
		inboundhttp.Dependency{Name: "kafka", Check: kafkaCheck},
	)
}

// migrationsDatabaseURL returns MIGRATIONS_DATABASE_URL when set (a direct
// DSN: golang-migrate's advisory lock does not survive PgBouncer transaction
// pooling), else databaseURL.
func migrationsDatabaseURL(databaseURL string) string {
	return getenv(postgres.EnvMigrationsDatabaseURL, databaseURL)
}
