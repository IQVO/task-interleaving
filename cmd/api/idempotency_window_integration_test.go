//go:build integration

package main

import (
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	inboundhttp "github.com/claudioed/task-interleaving/internal/adapters/inbound/http"
	"github.com/claudioed/task-interleaving/internal/adapters/outbound/postgres"
	"github.com/claudioed/task-interleaving/internal/testing/pgtest"
)

// stepClock is a clock the test moves by hand (safe for the server goroutines).
type stepClock struct{ ns atomic.Int64 }

func newStepClock(at time.Time) *stepClock {
	c := &stepClock{}
	c.ns.Store(at.UnixNano())
	return c
}

func (c *stepClock) Now() time.Time { return time.Unix(0, c.ns.Load()).UTC() }

func (c *stepClock) Advance(d time.Duration) { c.ns.Add(int64(d)) }

// carry (fleet audit 2026-11-21; ADR 0013 follow-up "no age check applies on
// replay"), the Postgres twin of the http package's
// TestAStoredAnswerReplaysOnlyWithinThe24HourWindow: over the real store a
// stored answer replays at exactly 24 h; a microsecond later the same key and
// body execute again, the new answer replaces the expired row (still one row
// for the key) and is what replays afterwards.
func TestPostgresAStoredAnswerReplaysOnlyWithinThe24HourWindow(t *testing.T) {
	clock := newStepClock(time.Date(2026, 11, 21, 8, 0, 0, 0, time.UTC))
	svc := bootPostgresOver(t, func(ad *adapters, url string) {
		ad.idempotency = inboundhttp.NewIdempotency(postgres.NewIdempotencyStore(pgtest.Open(t, url)), ad.uow, clock).Middleware
	})
	svc.declare(t)

	status, first, hdr := svc.simulate(t, simulation, "k-window")
	if status != http.StatusOK || hdr.Get("Idempotency-Replayed") != "" {
		t.Fatalf("first = %d %s %v, want a fresh 200", status, first, hdr)
	}
	clock.Advance(24 * time.Hour)
	if status, body, hdr := svc.simulate(t, simulation, "k-window"); status != http.StatusOK || body != first || hdr.Get("Idempotency-Replayed") != "true" {
		t.Fatalf("at exactly 24 h = %d %s %v, want the stored answer replayed", status, body, hdr)
	}

	clock.Advance(time.Microsecond)
	status, late, hdr := svc.simulate(t, simulation, "k-window")
	if status != http.StatusOK || hdr.Get("Idempotency-Replayed") != "" {
		t.Fatalf("past 24 h = %d %s %v, want a fresh execution", status, late, hdr)
	}
	if n := svc.count("idempotency_keys"); n != 1 {
		t.Fatalf("idempotency rows = %d, want 1: the expired answer is replaced", n)
	}
	if n := svc.count("idempotency_keys WHERE created_at = '2026-11-22 08:00:00.000001+00'"); n != 1 {
		t.Fatalf("the key row was not replaced by the new answer (rows at the new instant: %d)", n)
	}
	if status, body, hdr := svc.simulate(t, simulation, "k-window"); status != http.StatusOK || body != late || hdr.Get("Idempotency-Replayed") != "true" {
		t.Fatalf("after the re-execution = %d %s %v, want the NEW answer replayed", status, body, hdr)
	}
	if status, body, _ := svc.simulate(t, `{"stationId":"STN-08","fromLocation":"A-01"}`, "k-window"); status != http.StatusConflict || !strings.Contains(body, "/idempotency-key-reused") {
		t.Fatalf("another body under the live key = %d %s, want 409 idempotency-key-reused", status, body)
	}
}
