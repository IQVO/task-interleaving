package memory_test

import (
	"testing"

	"github.com/claudioed/task-interleaving/internal/adapters/outbound/memory"
	"github.com/claudioed/task-interleaving/internal/application/ports"
	"github.com/claudioed/task-interleaving/internal/testing/repocontract"
)

// The behavioural contract the Postgres adapters pass against a real database
// (internal/adapters/outbound/postgres, integration tag): the same cases here.

func TestPolicyRepoContract(t *testing.T) {
	repocontract.PolicyRepo(t, func(*testing.T) ports.PolicyRepo { return memory.NewPolicyRepo() })
}

func TestStationTripRepoContract(t *testing.T) {
	repocontract.StationTripRepo(t, func(*testing.T) ports.StationTripRepo { return memory.NewStationTripRepo() })
}

func TestTaskPoolContract(t *testing.T) {
	repocontract.TaskPool(t, func(*testing.T) ports.TaskPool { return memory.NewTaskPool() })
}

func TestLocationBookContract(t *testing.T) {
	repocontract.LocationBook(t, func(*testing.T) ports.LocationBook { return memory.NewLocationBook() })
}

func TestProcessedEventsContract(t *testing.T) {
	repocontract.ProcessedEvents(t, func(*testing.T) ports.ProcessedEvents { return memory.NewProcessedEventRepo() })
}

func TestIdempotencyStoreContract(t *testing.T) {
	repocontract.IdempotencyStore(t, func(*testing.T) ports.IdempotencyStore { return memory.NewIdempotencyStore() })
}
