//go:build integration

package main

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	inboundhttp "github.com/claudioed/task-interleaving/internal/adapters/inbound/http"
	"github.com/claudioed/task-interleaving/internal/adapters/outbound/postgres"
	"github.com/claudioed/task-interleaving/internal/adapters/outbound/postgres/pgtx"
	"github.com/claudioed/task-interleaving/internal/adapters/outbound/system"
	"github.com/claudioed/task-interleaving/internal/testing/pgtest"
	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// The tcpostgres reference satisfies internal/architecture's
// TestPostgresIntegrationTestsUseTestcontainers: the database comes from
// pgtest.NewPool, which starts a testcontainers Postgres.
var _ = tcpostgres.Run

// poisonTx does what a mapped lost race does to the middleware's transaction:
// a statement fails on the server (unique violation, 23505) on the transaction
// the unit of work bound into the request context, and the handler goes on to
// answer from there. The transaction is ABORTED from then on.
func poisonTx(t *testing.T, r *http.Request) {
	t.Helper()
	tx, ok := pgtx.From(r.Context())
	if !ok {
		t.Fatal("the middleware did not bind a transaction into the request context")
	}
	const ins = `INSERT INTO idempotency_keys (key, fingerprint, status, content_type, body, created_at)
		VALUES ('dup', 'f', 200, '', ''::bytea, now())`
	for i := 0; i < 2; i++ {
		if _, err := tx.Exec(r.Context(), ins); err != nil {
			return // the second insert aborts the transaction
		}
	}
	t.Fatal("the duplicate insert did not abort the transaction")
}

func poisonCount(t *testing.T, pool *pgxpool.Pool, key string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM idempotency_keys WHERE key = $1`, key).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func poisonMiddleware(t *testing.T, h http.Handler) (http.Handler, *pgxpool.Pool) {
	t.Helper()
	pool := pgtest.NewPool(t)
	mw := inboundhttp.NewIdempotency(postgres.NewIdempotencyStore(pool), postgres.NewUnitOfWork(pool), system.Clock{})
	return mw.Middleware(h), pool
}

func keyed(key string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/recommendations", nil)
	req.Header.Set(inboundhttp.IdempotencyKeyHeader, key)
	return req
}

// A mapped 4xx over an aborted transaction is still recorded and returned
// (never a 500), replays, and never re-runs the handler.
func TestIdempotencyA409OverAnAbortedTransactionIsRecorded(t *testing.T) {
	var calls atomic.Int64
	mw, pool := poisonMiddleware(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		poisonTx(t, r)
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"status":409}`))
	}))

	first := httptest.NewRecorder()
	mw.ServeHTTP(first, keyed("k-poison"))
	if first.Code != http.StatusConflict {
		t.Fatalf("first = %d %s, want the truthful 409", first.Code, first.Body.String())
	}
	var status int
	if err := pool.QueryRow(t.Context(), `SELECT status FROM idempotency_keys WHERE key = 'k-poison'`).Scan(&status); err != nil || status != http.StatusConflict {
		t.Fatalf("409 outcome committed = %d, %v", status, err)
	}
	if n := poisonCount(t, pool, "dup"); n != 0 {
		t.Fatalf("the handler's partial write survived: %d rows", n)
	}

	replay := httptest.NewRecorder()
	mw.ServeHTTP(replay, keyed("k-poison"))
	if replay.Code != http.StatusConflict || replay.Header().Get(inboundhttp.IdempotencyReplayedHeader) != "true" {
		t.Fatalf("replay = %d replayed=%q", replay.Code, replay.Header().Get(inboundhttp.IdempotencyReplayedHeader))
	}
	if replay.Body.String() != first.Body.String() || calls.Load() != 1 {
		t.Fatalf("replay body %q, handler ran %d times, want identical body and 1 run", replay.Body.String(), calls.Load())
	}
}

// A 2xx over an aborted transaction is a lie: never returned, never cached;
// the same key retries the real work.
func TestIdempotencyA2xxOverAnAbortedTransactionIsNeverCached(t *testing.T) {
	var calls atomic.Int64
	mw, pool := poisonMiddleware(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			poisonTx(t, r)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))

	first := httptest.NewRecorder()
	mw.ServeHTTP(first, keyed("k-lie"))
	if first.Code != http.StatusInternalServerError {
		t.Fatalf("first = %d %s, want 500", first.Code, first.Body.String())
	}
	if n := poisonCount(t, pool, "k-lie"); n != 0 {
		t.Fatalf("a failed success left %d idempotency rows", n)
	}
	second := httptest.NewRecorder()
	mw.ServeHTTP(second, keyed("k-lie"))
	if second.Code != http.StatusOK || second.Header().Get(inboundhttp.IdempotencyReplayedHeader) != "" || calls.Load() != 2 {
		t.Fatalf("retry = %d, handler ran %d times, want a fresh 200 on run 2", second.Code, calls.Load())
	}
}
