//go:build integration

package analyticsstore

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/claudioed/task-interleaving/internal/analytics/report"
	"github.com/claudioed/task-interleaving/internal/testing/pgtest"
)

// The infrastructure comes from testcontainers (pgtest), never from the
// environment: every test gets a private, EMPTY database that this package then
// migrates with its own embedded analytical migrations.
func TestMain(m *testing.M) {
	code := m.Run()
	pgtest.Shutdown()
	os.Exit(code)
}

// pgStore is the Postgres writer and reader of one fresh analytical database.
type pgStore struct {
	*Projection
	*Reader
	url string
}

func newPostgres(t *testing.T) *pgStore {
	t.Helper()
	url := pgtest.EmptyURL(t)
	// Twice: boot runs the migrations on every start, so the second run must be
	// a no-op (ErrNoChange).
	for range 2 {
		if err := RunMigrations(url); err != nil {
			t.Fatalf("run analytics migrations: %v", err)
		}
	}
	ctx := context.Background()
	writer, err := NewPool(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(writer.Close)
	reader, err := NewReadOnlyPool(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reader.Close)
	return &pgStore{Projection: NewProjection(writer), Reader: NewReader(reader), url: url}
}

func TestPostgres_Contract(t *testing.T) {
	runContract(t, func(t *testing.T) store { return newPostgres(t) })
}

// The READER pool cannot write, even with a role that could.
func TestPostgres_ReadOnlyPoolRefusesWrites(t *testing.T) {
	s := newPostgres(t)
	ro, err := NewReadOnlyPool(context.Background(), s.url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ro.Close)
	if _, err := ro.Exec(context.Background(), `INSERT INTO analytics_processed_events (event_id, event_type, occurred_at) VALUES ('x', 'x', now())`); err == nil {
		t.Fatal("the read-only pool accepted a write")
	}
}

// Concurrent deliveries of the same event id fold exactly once.
func TestPostgres_ConcurrentReplayAppliesOnce(t *testing.T) {
	s := newPostgres(t)
	e := issued("e1", "hint-1", "SITE-1", "2026-10-05T10:00:00Z")
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		applied int
	)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := s.Apply(context.Background(), e)
			if err != nil {
				t.Error(err)
				return
			}
			if ok {
				mu.Lock()
				applied++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if applied != 1 {
		t.Fatalf("applied %d times, want exactly once", applied)
	}
}

// A database-level integrity violation is a deterministic rejection, not a
// transient failure the consumer would retry forever.
func TestPostgres_IntegrityViolationIsRejected(t *testing.T) {
	s := newPostgres(t)
	// Bypass Validate to prove the SQLSTATE classification on its own.
	pool := pgtest.Open(t, s.url)
	if _, err := pool.Exec(context.Background(), `INSERT INTO hint_facts (hint_id, station_id, site_id) VALUES ('h', NULL, 's')`); err == nil {
		t.Fatal("hint_facts accepted a NULL station_id")
	} else if got := classify(err); !isRejected(got) {
		t.Fatalf("classify(%v) = %v, want ErrRejected", err, got)
	}
}

func isRejected(err error) bool { return err != nil && errors.Is(err, report.ErrRejected) }
