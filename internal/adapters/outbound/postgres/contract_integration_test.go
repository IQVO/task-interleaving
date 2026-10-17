//go:build integration

package postgres_test

import (
	"testing"

	"github.com/claudioed/task-interleaving/internal/adapters/outbound/postgres"
	"github.com/claudioed/task-interleaving/internal/application/ports"
	"github.com/claudioed/task-interleaving/internal/testing/pgtest"
	"github.com/claudioed/task-interleaving/internal/testing/repocontract"
)

// The same behavioural contract the in-memory adapters pass, run against a
// real Postgres: every contract case gets its own database.

func TestPolicyRepoContract(t *testing.T) {
	repocontract.PolicyRepo(t, func(t *testing.T) ports.PolicyRepo {
		return postgres.NewPolicyRepo(pgtest.NewPool(t))
	})
}

func TestStationTripRepoContract(t *testing.T) {
	repocontract.StationTripRepo(t, func(t *testing.T) ports.StationTripRepo {
		return postgres.NewStationTripRepo(pgtest.NewPool(t))
	})
}

func TestTaskPoolContract(t *testing.T) {
	repocontract.TaskPool(t, func(t *testing.T) ports.TaskPool {
		return postgres.NewTaskPool(pgtest.NewPool(t))
	})
}

func TestLocationBookContract(t *testing.T) {
	repocontract.LocationBook(t, func(t *testing.T) ports.LocationBook {
		return postgres.NewLocationBook(pgtest.NewPool(t))
	})
}

func TestProcessedEventsContract(t *testing.T) {
	repocontract.ProcessedEvents(t, func(t *testing.T) ports.ProcessedEvents {
		return postgres.NewProcessedEventRepo(pgtest.NewPool(t))
	})
}

func TestIdempotencyStoreContract(t *testing.T) {
	repocontract.IdempotencyStore(t, func(t *testing.T) ports.IdempotencyStore {
		return postgres.NewIdempotencyStore(pgtest.NewPool(t))
	})
}
