//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/task-interleaving/internal/adapters/outbound/postgres"
	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
	"github.com/claudioed/task-interleaving/internal/testing/pgtest"
)

var errBoom = errors.New("boom")

// race runs fn(0..n-1) concurrently, released together, and returns the n
// results in order.
func race(n int, fn func(i int) error) []error {
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results[i] = fn(i)
		}()
	}
	close(start)
	wg.Wait()
	return results
}

// wins counts the nil results; every other result must be one of the allowed
// losing errors.
func wins(t *testing.T, results []error, allowed ...error) int {
	t.Helper()
	won := 0
	for _, err := range results {
		if err == nil {
			won++
			continue
		}
		if !matchesAny(err, allowed) {
			t.Fatalf("a loser must lose with one of %v, got %v", allowed, err)
		}
	}
	return won
}

func matchesAny(err error, allowed []error) bool {
	for _, a := range allowed {
		if errors.Is(err, a) {
			return true
		}
	}
	return false
}

// testClock is a ports.Clock the test moves by hand.
type testClock struct{ now time.Time }

func (c *testClock) Now() time.Time { return c.now }

// seqIDs mints hint-1, hint-2, ... (ports.IDGenerator).
type seqIDs struct {
	mu sync.Mutex
	n  int
}

func (s *seqIDs) NewHintID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	return fmt.Sprintf("hint-%d", s.n)
}

// recordingSink records the names of the facts it is handed (ports.EventSink).
type recordingSink struct {
	mu    sync.Mutex
	names []string
}

func (s *recordingSink) PublishTripEvents(_ context.Context, events ...stationtrip.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range events {
		s.names = append(s.names, e.EventName())
	}
	return nil
}

func (s *recordingSink) PublishPolicyDeclared(context.Context, repository.PolicyDeclaration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.names = append(s.names, "InterleavingPolicyDeclared")
	return nil
}

func (s *recordingSink) recorded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.names...)
}

// failingSink is an EventSink whose enqueue always fails: the effect of a use
// case that ran before it must roll back with it.
type failingSink struct{}

func (failingSink) PublishTripEvents(context.Context, ...stationtrip.Event) error {
	return errBoom
}

func (failingSink) PublishPolicyDeclared(context.Context, repository.PolicyDeclaration) error {
	return errBoom
}

// stores are the Postgres adapters over one database.
type stores struct {
	pool      *pgxpool.Pool
	policies  *postgres.PolicyRepo
	trips     *postgres.StationTripRepo
	tasks     *postgres.TaskPool
	book      *postgres.LocationBook
	processed *postgres.ProcessedEventRepo
	uow       *postgres.UnitOfWork
	count     func(table string) int
}

func newStores(t *testing.T) stores {
	t.Helper()
	pool := pgtest.NewPool(t)
	return stores{
		pool:      pool,
		policies:  postgres.NewPolicyRepo(pool),
		trips:     postgres.NewStationTripRepo(pool),
		tasks:     postgres.NewTaskPool(pool),
		book:      postgres.NewLocationBook(pool),
		processed: postgres.NewProcessedEventRepo(pool),
		uow:       postgres.NewUnitOfWork(pool),
		count:     func(table string) int { return pgtest.Count(t, pool, table) },
	}
}
