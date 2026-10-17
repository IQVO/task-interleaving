//go:build integration

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	inboundhttp "github.com/claudioed/task-interleaving/internal/adapters/inbound/http"
	"github.com/claudioed/task-interleaving/internal/testing/pgtest"
)

// The infrastructure comes from testcontainers, never from the environment.
func TestMain(m *testing.M) {
	code := m.Run()
	pgtest.Shutdown()
	os.Exit(code)
}

// pgService is the service booted over a real Postgres.
type pgService struct {
	srv *httptest.Server
	ad  adapters
	// count returns the number of rows of a table (optionally with a WHERE
	// clause) of the service's database.
	count func(table string) int
}

// bootPostgres boots the service the way main does against a database with NO
// migrations: boot applies them, wires the Postgres adapters and serves.
func bootPostgres(t *testing.T) pgService {
	t.Helper()
	url := pgtest.EmptyURL(t)
	ad, closeAdapters, err := buildAdapters(t.Context(), url, url, discardLogger())
	if err != nil {
		t.Fatalf("buildAdapters: %v", err)
	}
	t.Cleanup(closeAdapters)
	pool := pgtest.Open(t, url)
	seedWorld(t, ad)
	srv := httptest.NewServer(inboundhttp.NewRouter(buildServer(ad, &inboundhttp.Readiness{})))
	t.Cleanup(srv.Close)
	return pgService{srv: srv, ad: ad, count: func(table string) int { return pgtest.Count(t, pool, table) }}
}

func (s pgService) declare(t *testing.T) {
	t.Helper()
	if status, body, _ := do(t, http.MethodPut, s.srv.URL+"/sites/SITE-1/policy", declareBody); status != http.StatusOK {
		t.Fatalf("declare = %d %s", status, body)
	}
}

func (s pgService) simulate(t *testing.T, body, key string) (int, string, http.Header) {
	t.Helper()
	var headers []string
	if key != "" {
		headers = []string{"Idempotency-Key", key}
	}
	return do(t, http.MethodPost, s.srv.URL+"/recommendations", body, headers...)
}

func TestPostgresModeMigratesEnforcesIdempotencyAndWritesNoOutboxForTheSimulation(t *testing.T) {
	svc := bootPostgres(t)
	if svc.ad.outbox == nil || svc.ad.idempotency == nil {
		t.Fatal("postgres mode wires the outbox store and the idempotency middleware")
	}
	svc.declare(t)
	if status, body, _ := svc.simulate(t, simulation, ""); status != http.StatusBadRequest || !strings.Contains(body, "idempotency-key-required") {
		t.Fatalf("no key = %d %s", status, body)
	}
	status, body, hdr := svc.simulate(t, simulation, "k-1")
	if status != http.StatusOK || !strings.Contains(body, `"taskId":"task-9c2e"`) {
		t.Fatalf("simulate = %d %s", status, body)
	}
	status2, body2, hdr2 := svc.simulate(t, simulation, "k-1")
	if status2 != status || body2 != body || hdr2.Get("Content-Type") != hdr.Get("Content-Type") {
		t.Fatalf("replay differs: %d %s %v", status2, body2, hdr2)
	}
	if status, body, _ := svc.simulate(t, `{"stationId":"STN-08","fromLocation":"A-01"}`, "k-1"); status != http.StatusUnprocessableEntity || !strings.Contains(body, "idempotency-key-reused") {
		t.Fatalf("reused key = %d %s", status, body)
	}
	// Only the policy declaration (analytics) is an outbox fact: the
	// simulation issues and enqueues nothing.
	if n := svc.count("outbox_events"); n != 1 {
		t.Fatalf("outbox rows = %d, want 1", n)
	}
	if n := svc.count("idempotency_keys"); n != 1 {
		t.Fatalf("idempotency rows = %d, want 1", n)
	}
	if n := svc.count("hints") + svc.count("station_trips"); n != 0 {
		t.Fatalf("the simulation wrote %d hint/station rows", n)
	}
}

// The policy PUT is If-Match guarded on real Postgres: ETag out, 412 for a
// missing or stale value, and a refused PUT leaves no row and no outbox fact.
func TestPostgresPolicyPutIsVersionGuarded(t *testing.T) {
	svc := bootPostgres(t)
	url := svc.srv.URL + "/sites/SITE-1/policy"
	if status, _, hdr := do(t, http.MethodPut, url, declareBody); status != http.StatusOK || hdr.Get("ETag") != `"1"` {
		t.Fatalf("first declaration = %d %v", status, hdr)
	}
	for name, headers := range map[string][]string{"missing": nil, "stale": {"If-Match", `"9"`}} {
		if status, body, _ := do(t, http.MethodPut, url, declareBody, headers...); status != http.StatusPreconditionFailed || !strings.Contains(body, "version-mismatch") {
			t.Fatalf("%s If-Match = %d %s", name, status, body)
		}
	}
	if status, body, _ := do(t, http.MethodPut, url, `{"maxDetourM":-1}`, "If-Match", `"1"`); status != http.StatusBadRequest {
		t.Fatalf("invalid body = %d %s", status, body)
	}
	if n := svc.count("outbox_events"); n != 1 {
		t.Fatalf("refused PUTs left outbox rows: %d, want 1", n)
	}
	if status, _, hdr := do(t, http.MethodPut, url, declareBody, "If-Match", `"1"`); status != http.StatusOK || hdr.Get("ETag") != `"2"` {
		t.Fatalf("replacement = %d %v", status, hdr)
	}
	if n := svc.count("outbox_events"); n != 2 {
		t.Fatalf("outbox rows = %d, want one per accepted declaration", n)
	}
}

// A 4xx answer rolls its unit of work back: the rejected request leaves no
// row behind, and the rejection itself replays.
func TestPostgresRejectionLeavesNoEffectAndReplays(t *testing.T) {
	svc := bootPostgres(t) // no policy declared yet
	status, body, _ := svc.simulate(t, simulation, "k-no-policy")
	if status != http.StatusUnprocessableEntity || !strings.Contains(body, "/policy-not-declared") {
		t.Fatalf("simulate without a policy = %d %s", status, body)
	}
	for _, table := range []string{"interleaving_policies", "hints", "station_trips", "outbox_events"} {
		if n := svc.count(table); n != 0 {
			t.Fatalf("%s has %d rows after a rejection", table, n)
		}
	}
	svc.declare(t) // the world changed, the key still means the rejection
	if status2, body2, _ := svc.simulate(t, simulation, "k-no-policy"); status2 != status || body2 != body {
		t.Fatalf("rejection replay differs: %d %s", status2, body2)
	}
	if n := svc.count("idempotency_keys"); n != 1 {
		t.Fatalf("idempotency rows = %d, want the stored rejection only", n)
	}
}

// Eight concurrent requests with one NEW key on real Postgres: every caller
// gets the same answer, exactly one answer is stored, and the simulation wrote
// nothing else (no hint, no station, no outbox row beyond the declaration).
func TestPostgresConcurrentRequestsWithOneKeyStoreOneAnswer(t *testing.T) {
	svc := bootPostgres(t)
	svc.declare(t)
	const n = 8
	type result struct {
		status int
		body   string
	}
	results := make([]result, n)
	var done atomic.Int32
	finished := make(chan struct{})
	for i := range n {
		go func() {
			results[i].status, results[i].body, _ = svc.simulate(t, simulation, "k-race")
			if done.Add(1) == n {
				close(finished)
			}
		}()
	}
	select {
	case <-finished:
	case <-time.After(60 * time.Second):
		t.Fatal("the concurrent requests did not finish")
	}
	for i, r := range results {
		if r.status != http.StatusOK || r.body != results[0].body {
			t.Fatalf("result %d = %d %s, want the original 200 %s", i, r.status, r.body, results[0].body)
		}
	}
	if got := svc.count("idempotency_keys"); got != 1 {
		t.Fatalf("idempotency rows = %d, want 1", got)
	}
	if got := svc.count("outbox_events") + svc.count("hints") + svc.count("station_trips"); got != 1 {
		t.Fatalf("outbox+hint+station rows = %d, want only the declaration's outbox row", got)
	}
}

// The expiry worker over real Postgres: the hint the completion issued is
// Expired after its ttl without any claim, and its analytics fact is enqueued
// in the same transaction.
func TestPostgresExpiryWorkerExpiresTheHintAndEnqueuesTheFact(t *testing.T) {
	svc := bootPostgres(t)
	svc.declare(t) // hintTtlSeconds: 1
	completeTask(t, svc.ad, "task-8a1f")
	if n := svc.count("hints WHERE state = 'Active'"); n != 1 {
		t.Fatalf("active hints = %d, want 1", n)
	}
	issued := svc.count("outbox_events") // declaration + issued (events + analytics)
	if issued != 3 {
		t.Fatalf("outbox rows after the completion = %d, want 3", issued)
	}

	w := startExpiryWorker(svc.ad.expireHints(), 20*time.Millisecond, discardLogger())
	defer stopWorkers(discardLogger(), []*worker{w})
	deadline := time.Now().Add(30 * time.Second)
	for svc.count("hints WHERE state = 'Expired'") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the expiry worker never expired the hint")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if n := svc.count("outbox_events"); n != issued+1 {
		t.Fatalf("outbox rows after expiry = %d, want %d (the Expired analytics fact)", n, issued+1)
	}
	if status, body, _ := do(t, http.MethodGet, svc.srv.URL+"/stations/STN-07", ""); status != http.StatusOK || strings.Contains(body, "activeHint") {
		t.Fatalf("station after expiry = %d %s", status, body)
	}
}

// The boot refuses to start when the database cannot be reached: it retries
// with backoff, then fails closed with the real cause.
func TestPostgresBootFailsClosedOnAnUnreachableDatabase(t *testing.T) {
	const dead = "postgres://nobody:***@127.0.0.1:1/none?sslmode=disable&connect_timeout=1"
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if _, closeAdapters, err := buildAdapters(ctx, dead, dead, discardLogger()); err == nil {
		closeAdapters()
		t.Fatal("an unreachable database must fail the boot")
	}
}
