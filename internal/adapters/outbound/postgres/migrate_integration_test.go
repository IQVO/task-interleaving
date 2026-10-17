//go:build integration

package postgres_test

import (
	"context"
	"strings"
	"testing"

	"github.com/claudioed/task-interleaving/internal/adapters/outbound/postgres"
	"github.com/claudioed/task-interleaving/internal/testing/pgtest"
)

func migrator(t *testing.T, url string) interface {
	Migrate(version uint) error
	Steps(n int) error
	Down() error
	Version() (uint, bool, error)
	Close() (error, error)
} {
	t.Helper()
	m, err := postgres.Migrator(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = m.Close() })
	return m
}

func mustNil(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestMigrationsApplyAndRunAgainAsANoOp(t *testing.T) {
	url := pgtest.EmptyURL(t)
	if got := pgtest.Schema(t, url); got != "" {
		t.Fatalf("the database must start empty, got:\n%s", got)
	}
	mustNil(t, postgres.RunMigrations(url))
	full := pgtest.Schema(t, url)
	for _, table := range []string{
		"interleaving_policies", "station_trips", "hints", "candidates", "task_pool",
		"zones", "locations", "processed_events", "outbox_events", "idempotency_keys",
	} {
		if !strings.Contains(full, "col "+table+".") {
			t.Fatalf("table %s missing from the migrated schema", table)
		}
	}
	mustNil(t, postgres.RunMigrations(url))
	if again := pgtest.Schema(t, url); again != full {
		t.Fatalf("a second run changed the schema:\n%s", again)
	}
}

// TestEveryDownRecreatesThePreviousSchemaExactly walks the migrations one step
// at a time in both directions and compares the schema at each version with the
// one first seen when going up.
func TestEveryDownRecreatesThePreviousSchemaExactly(t *testing.T) {
	url := pgtest.EmptyURL(t)
	m := migrator(t, url)
	seen := map[uint]string{0: pgtest.Schema(t, url)}
	var top uint
	for {
		if err := m.Steps(1); err != nil {
			break // no next migration
		}
		v, dirty, err := m.Version()
		mustNil(t, err)
		if dirty {
			t.Fatalf("version %d is dirty", v)
		}
		seen[v], top = pgtest.Schema(t, url), v
	}
	if top < 1 {
		t.Fatalf("expected at least one migration, top version is %d", top)
	}
	for v := top; v > 0; v-- {
		mustNil(t, m.Steps(-1))
		if got := pgtest.Schema(t, url); got != seen[v-1] {
			t.Fatalf("down from %d did not recreate version %d:\n got:\n%s\nwant:\n%s", v, v-1, got, seen[v-1])
		}
	}
	if seen[0] != "" {
		t.Fatalf("version 0 must be an empty schema, got:\n%s", seen[0])
	}
	mustNil(t, m.Migrate(top))
	if got := pgtest.Schema(t, url); got != seen[top] {
		t.Fatalf("up again does not reproduce version %d", top)
	}
}

// TestTheSchemaCarriesTheInvariantIndexesAndKeys pins the database rules the
// adapters rely on, so a later additive migration cannot quietly drop one.
func TestTheSchemaCarriesTheInvariantIndexesAndKeys(t *testing.T) {
	url := pgtest.EmptyURL(t)
	mustNil(t, postgres.RunMigrations(url))
	schema := pgtest.Schema(t, url)
	for _, want := range []string{
		"idx CREATE UNIQUE INDEX uq_hints_one_active_per_station ON public.hints USING btree (station_id) WHERE (state = 'Active'::text)",
		"con candidates UNIQUE (hint_id, task_id)",
		"con station_trips FOREIGN KEY (current_hint_id) REFERENCES hints(hint_id) DEFERRABLE INITIALLY DEFERRED deferrable=true",
		"con hints FOREIGN KEY (station_id) REFERENCES station_trips(station_id) deferrable=false",
		"con outbox_events UNIQUE (event_id, topic)",
		"con processed_events PRIMARY KEY (consumer, event_id)",
	} {
		if !strings.Contains(schema, want) {
			t.Errorf("the migrated schema lacks %q:\n%s", want, schema)
		}
	}
}

func TestMigrationsRunFromTheUnpooledURLOnly(t *testing.T) {
	// A bad DATABASE_URL (the pooled one) is irrelevant when the direct URL is
	// set: the binary migrates over MIGRATIONS_DATABASE_URL.
	direct := pgtest.EmptyURL(t)
	env := map[string]string{"MIGRATIONS_DATABASE_URL": direct, "DATABASE_URL": "postgres://nobody@127.0.0.1:1/none"}
	mustNil(t, postgres.RunMigrations(postgres.MigrationsURL(func(k string) string { return env[k] })))
	pool, err := postgres.NewPool(context.Background(), direct)
	mustNil(t, err)
	defer pool.Close()
	if n := pgtest.Count(t, pool, "hints"); n != 0 {
		t.Fatalf("rows = %d", n)
	}
}
