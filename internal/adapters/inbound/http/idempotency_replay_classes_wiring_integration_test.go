//go:build integration

package http_test

// The repo-specific half of the replay-class template (idempotency_replay_classes_test.go,
// rendered verbatim from warehouse-harness-template): the REAL middleware over a real Postgres
// (key row in the business transaction, ADR 0005). No driver package is named here: the pool comes
// from pgtest.NewPool by type inference (the shared testcontainers helper). The in-memory twin,
// run by the unit job, is idempotency_replay_classes_wiring_test.go.

import (
	"context"
	"errors"
	"net/http"
	"os"
	"testing"

	inboundhttp "github.com/claudioed/task-interleaving/internal/adapters/inbound/http"
	"github.com/claudioed/task-interleaving/internal/adapters/outbound/postgres"
	"github.com/claudioed/task-interleaving/internal/adapters/outbound/postgres/pgtx"
	"github.com/claudioed/task-interleaving/internal/adapters/outbound/system"
	"github.com/claudioed/task-interleaving/internal/testing/pgtest"
)

func TestMain(m *testing.M) {
	code := m.Run()
	pgtest.Shutdown()
	os.Exit(code)
}

// replayCatalogue is the production catalogue plus the synthetic OUTCOME probe.
func replayCatalogue() []problemEntry {
	entries := []problemEntry{{Slug: inboundhttp.OutcomeProbeSlug, Status: http.StatusConflict, Class: classOutcome}}
	for _, e := range inboundhttp.ReplayCatalogue() {
		entries = append(entries, problemEntry{Slug: e.Slug, Status: e.Status, Class: e.Class})
	}
	return entries
}

func newReplayOps(t *testing.T) replayOps {
	t.Helper()
	pool := pgtest.NewPool(t)
	must(t, pgtest.Exec(pool, `CREATE TABLE replay_probe (tag TEXT NOT NULL)`))
	count := func(t *testing.T, query, arg string) int {
		t.Helper()
		var n int
		pgtest.Scan(t, pool, query, []any{arg}, &n)
		return n
	}
	mw := inboundhttp.NewIdempotency(postgres.NewIdempotencyStore(pool), postgres.NewUnitOfWork(pool), system.Clock{})
	return replayOps{
		Catalogue:  replayCatalogue,
		Middleware: func(_ *testing.T, next http.Handler) http.Handler { return mw.Middleware(next) },
		Refuse: func(w http.ResponseWriter, r *http.Request, e problemEntry) {
			inboundhttp.RefuseForTest(w, r, e.Slug)
		},
		Write: func(ctx context.Context, tag string) error {
			tx, ok := pgtx.From(ctx)
			if !ok {
				return errors.New("the request context carries no unit of work")
			}
			_, err := tx.Exec(ctx, `INSERT INTO replay_probe (tag) VALUES ($1)`, tag)
			return err
		},
		Committed: func(t *testing.T, tag string) int {
			return count(t, `SELECT count(*) FROM replay_probe WHERE tag = $1`, tag)
		},
		KeyRows: func(t *testing.T, key string) int {
			return count(t, `SELECT count(*) FROM idempotency_keys WHERE key = $1`, key)
		},
	}
}
