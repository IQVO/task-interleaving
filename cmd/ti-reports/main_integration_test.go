//go:build integration

package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/claudioed/task-interleaving/internal/adapters/outbound/analyticsstore"
	"github.com/claudioed/task-interleaving/internal/analytics/report"
	"github.com/claudioed/task-interleaving/internal/testing/pgtest"
)

func TestMain(m *testing.M) {
	code := m.Run()
	pgtest.Shutdown()
	os.Exit(code)
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().String()
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func getJSON(t *testing.T, raw string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Get(raw)
	if err != nil {
		// Before the listener binds (and after shutdown) the dial is refused;
		// callers polling a probe treat 0 as "not up yet".
		return 0, nil
	}
	defer func() { _ = resp.Body.Close() }()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("GET %s: decode body: %v", raw, err)
	}
	return resp.StatusCode, body
}

// stopWithSIGTERM stops a signal-driven run(): SIGTERM to self, retried until
// run returns. The test's own handler is registered before the first signal
// and NEVER unregistered — unregistering while a retried signal is still in
// flight would restore the default disposition and kill the test binary.
func stopWithSIGTERM(t *testing.T, done <-chan error) {
	t.Helper()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM)
	deadline := time.Now().Add(30 * time.Second)
	for {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("a SIGTERM must drain the reports server cleanly, got %v", err)
			}
			return
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the reports server did not stop after SIGTERM")
		}
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		time.Sleep(100 * time.Millisecond)
	}
}

// The real entry point (ADR 0008) over an empty database migrated with the
// repo's own embedded analytics migrations and seeded through the repo's own
// analytics writer: /healthz answers, the documented hint-adoption day row and
// freshness come back from the seeded facts, the documented 400s hold, and a
// SIGTERM drains cleanly.
func TestReportsServeSeededFactsAndStopCleanly(t *testing.T) {
	db := pgtest.EmptyURL(t)
	if err := analyticsstore.RunMigrations(db); err != nil {
		t.Fatalf("run analytics migrations: %v", err)
	}
	ctx := context.Background()
	writer, err := analyticsstore.NewPool(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	projection := analyticsstore.NewProjection(writer)

	// Two hints, one consumed 20 s after issue and one expired, plus a policy
	// declaration: the smallest facts the adoption questions need (ADR 0008).
	base := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	apply := func(e report.Event) {
		t.Helper()
		if applied, err := projection.Apply(ctx, e); err != nil || !applied {
			t.Fatalf("seed %s %s: applied=%v err=%v", e.Kind, e.EventID, applied, err)
		}
	}
	apply(report.Event{Kind: report.KindPolicyDeclared, EventID: "seed-p", At: base.Add(-time.Minute), SiteID: "SITE-R1"})
	apply(report.Event{Kind: report.KindHintIssued, EventID: "seed-i1", At: base, HintID: "hint-r1", Station: "STN-R1", SiteID: "SITE-R1",
		ExpiresAt: base.Add(90 * time.Second), CandidateCount: 3})
	apply(report.Event{Kind: report.KindHintConsumed, EventID: "seed-c1", At: base.Add(20 * time.Second), HintID: "hint-r1", Station: "STN-R1", SiteID: "SITE-R1",
		Rank: 1, DetourM: 4.5})
	apply(report.Event{Kind: report.KindHintIssued, EventID: "seed-i2", At: base.Add(time.Minute), HintID: "hint-r2", Station: "STN-R1", SiteID: "SITE-R1",
		ExpiresAt: base.Add(150 * time.Second), CandidateCount: 2})
	apply(report.Event{Kind: report.KindHintExpired, EventID: "seed-x2", At: base.Add(3 * time.Minute), HintID: "hint-r2", Station: "STN-R1", SiteID: "SITE-R1"})

	addr := freeAddr(t)
	t.Setenv("ANALYTICS_DATABASE_URL", db)
	t.Setenv("HTTP_ADDR", addr)
	t.Setenv("LOG_LEVEL", "error")
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- run() }()

	root := "http://" + addr
	waitUntil(t, "/healthz 200", func() bool {
		code, _ := getJSON(t, root+"/healthz")
		return code == http.StatusOK
	})

	day := url.Values{"from": {"2026-11-03T00:00:00Z"}, "to": {"2026-11-04T00:00:00Z"}, "granularity": {"day"}}
	code, body := getJSON(t, root+"/reports/hint-adoption?"+day.Encode())
	if code != http.StatusOK {
		t.Fatalf("hint-adoption = %d %v", code, body)
	}
	days, _ := body["days"].([]any)
	if len(days) != 1 {
		t.Fatalf("hint-adoption days = %v, want exactly the dense 2026-11-03 bucket", body["days"])
	}
	row, _ := days[0].(map[string]any)
	want := map[string]any{
		"day": "2026-11-03", "hints_issued": 2.0, "hints_consumed": 1.0, "hints_expired": 1.0,
		"adoption_rate": 0.5, "consumed_first_rank": 1.0, "mean_detour_m": 4.5,
	}
	for k, v := range want {
		if row[k] != v {
			t.Fatalf("hint-adoption day %s = %v, want %v", k, row[k], v)
		}
	}
	ttc, _ := row["time_to_consume"].(map[string]any)
	if ttc["count"] != 1.0 || ttc["p50_seconds"] != 20.0 || ttc["p95_seconds"] != 20.0 {
		t.Fatalf("time_to_consume = %v, want count 1, p50 20 s, p95 20 s", row["time_to_consume"])
	}

	// siteId narrows every counter to one site (ADR 0008 §4).
	narrowed := url.Values{"from": {"2026-11-03T00:00:00Z"}, "to": {"2026-11-04T00:00:00Z"}, "siteId": {"SITE-NONE"}}
	code, body = getJSON(t, root+"/reports/hint-adoption?"+narrowed.Encode())
	days, _ = body["days"].([]any)
	if code != http.StatusOK || len(days) != 1 || days[0].(map[string]any)["hints_issued"] != 0.0 ||
		days[0].(map[string]any)["adoption_rate"] != nil {
		t.Fatalf("hint-adoption narrowed to another site = %d %v", code, body)
	}

	// An inverted range is the documented 400 (RFC 7807).
	code, body = getJSON(t, root+"/reports/hint-adoption?from=2026-11-04T00:00:00Z&to=2026-11-03T00:00:00Z")
	if code != http.StatusBadRequest {
		t.Fatalf("inverted range = %d %v, want 400", code, body)
	}

	// Freshness: as_of is the newest applied CloudEvents time, lag never negative.
	code, body = getJSON(t, root+"/reports/freshness")
	if code != http.StatusOK || body["as_of"] != "2026-11-03T10:03:00Z" {
		t.Fatalf("freshness = %d %v", code, body)
	}
	if lag, _ := body["lag_seconds"].(float64); lag < 0 {
		t.Fatalf("freshness lag_seconds = %v, never negative", body["lag_seconds"])
	}

	stopWithSIGTERM(t, done)
}
