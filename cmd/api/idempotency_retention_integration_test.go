//go:build integration

package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	inboundhttp "github.com/claudioed/task-interleaving/internal/adapters/inbound/http"
	"github.com/claudioed/task-interleaving/internal/adapters/outbound/postgres"
	"github.com/claudioed/task-interleaving/internal/testing/pgtest"
)

// ADR 0013, Amendment 2 (idempotency retention, 2026-11-22): a stored answer
// replays for the 24 h window only, but nothing deleted the rows the window
// had already expired, so idempotency_keys grew forever. These tests pin the
// retention over the real router and a real Postgres: the purge deletes the
// expired rows and keeps the live ones, a replay of an expired key EXECUTES
// (the age guard of Amendment 1), and a purge racing a request that holds a
// key neither blocks on it nor breaks it.

// seedExpiredKey inserts a key row created age ago (before the replay
// window), as traffic from a previous day would have left.
func seedExpiredKey(t *testing.T, url, key string, age time.Duration) {
	t.Helper()
	pgtest.Exec(pgtest.Open(t, url), `INSERT INTO idempotency_keys (key, fingerprint, status, content_type, body, created_at)
		VALUES ($1, 'stale', 200, 'application/json', '\x7b7d', now() - $2::interval)`, key, age.String())
}

// TestPostgresTheRetentionPurgeDeletesExpiredRowsAndKeepsLiveOnes: two keys
// answer at T and T+1 h; at T+24 h + 1 µs the first is outside the window and
// the second is one hour short of it. One purge pass (the exact pass the
// retention worker runs every hour) deletes exactly the expired row; the live
// answer still replays; the expired key, replayed, EXECUTES again and its new
// answer is what replays afterwards.
func TestPostgresTheRetentionPurgeDeletesExpiredRowsAndKeepsLiveOnes(t *testing.T) {
	clock := newStepClock(time.Date(2026, 11, 22, 8, 0, 0, 0, time.UTC))
	svc := bootPostgresOver(t, func(ad *adapters, _ string) {
		ad.idempotency = inboundhttp.NewIdempotency(ad.idempotencyStore, ad.uow, clock).Middleware
	})
	svc.declare(t)

	if status, first, hdr := svc.simulate(t, simulation, "k-old"); status != http.StatusOK || hdr.Get("Idempotency-Replayed") != "" {
		t.Fatalf("k-old first = %d %s %v, want a fresh 200", status, first, hdr)
	}
	clock.Advance(time.Hour)
	if status, live, hdr := svc.simulate(t, simulation, "k-live"); status != http.StatusOK || hdr.Get("Idempotency-Replayed") != "" {
		t.Fatalf("k-live first = %d %s %v, want a fresh 200", status, live, hdr)
	}
	// k-old is now 24 h + 1 µs old (outside the window); k-live is 23 h old.
	clock.Advance(24*time.Hour + time.Microsecond)

	if status, body, hdr := svc.simulate(t, simulation, "k-live"); status != http.StatusOK || hdr.Get("Idempotency-Replayed") != "true" {
		t.Fatalf("k-live before the purge = %d %s %v, want the stored answer replayed", status, body, hdr)
	}
	deleted, err := purgeIdempotencyKeys(t.Context(), svc.ad.idempotencyStore, clock, idempotencyPurgeBatch)
	if err != nil || deleted != 1 {
		t.Fatalf("purge = %d %v, want 1 (only the expired row)", deleted, err)
	}
	if n := svc.count("idempotency_keys"); n != 1 {
		t.Fatalf("idempotency rows after the purge = %d, want 1 (the live one)", n)
	}
	if n := svc.count("idempotency_keys WHERE key = 'k-live'"); n != 1 {
		t.Fatalf("the live row was deleted: rows for k-live = %d, want 1", n)
	}
	if again, err := purgeIdempotencyKeys(t.Context(), svc.ad.idempotencyStore, clock, idempotencyPurgeBatch); err != nil || again != 0 {
		t.Fatalf("second purge = %d %v, want 0: nothing expired is left", again, err)
	}

	// The age guard: an expired key never answers, not even between expiry
	// and the sweep (here: after it). The same key and body execute again.
	status, fresh, hdr := svc.simulate(t, simulation, "k-old")
	if status != http.StatusOK || hdr.Get("Idempotency-Replayed") != "" {
		t.Fatalf("replay of the expired k-old = %d %s %v, want a fresh execution", status, fresh, hdr)
	}
	if n := svc.count("idempotency_keys"); n != 2 {
		t.Fatalf("idempotency rows after the re-execution = %d, want 2 (k-live + k-old's new answer)", n)
	}
	if status, body, hdr := svc.simulate(t, simulation, "k-old"); status != http.StatusOK || body != fresh || hdr.Get("Idempotency-Replayed") != "true" {
		t.Fatalf("replay of the re-executed k-old = %d %s %v, want the NEW answer replayed", status, body, hdr)
	}
}

// TestPostgresThePurgeSkipsAndNeverWaitsOnAKeyARequestHolds: a request whose
// Save replaced an expired row is parked INSIDE its open transaction, so it
// holds the key's row lock until it commits. The purge (under a 10 s bounded
// context, so a wait would fail the test) must complete without blocking,
// delete the OTHER expired row and skip the held one; the parked request then
// commits a fresh 200 whose answer replays afterwards.
func TestPostgresThePurgeSkipsAndNeverWaitsOnAKeyARequestHolds(t *testing.T) {
	var parked *parkingStore
	var url string
	svc := bootPostgresOver(t, func(ad *adapters, dbURL string) {
		url = dbURL
		parked = newParkingStore(postgres.NewIdempotencyStore(pgtest.Open(t, dbURL)))
		ad.idempotency = inboundhttp.NewIdempotency(parked, ad.uow, ad.clock).Middleware
	})
	svc.declare(t)
	seedExpiredKey(t, url, "k-held", 48*time.Hour)
	seedExpiredKey(t, url, "k-other", 30*time.Hour)

	held := simulateAsync(svc.srv.URL, simulation, "k-held")
	select {
	case <-parked.parked:
	case <-time.After(inflightTimeout):
		t.Fatalf("the request never reached its key Save")
	}

	// The purge overlaps the held key: it must return promptly (FOR UPDATE
	// SKIP LOCKED skips the row the open transaction holds) and delete only
	// the other expired row.
	purgeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	deleted, err := purgeIdempotencyKeys(purgeCtx, svc.ad.idempotencyStore, svc.ad.clock, idempotencyPurgeBatch)
	if err != nil || deleted != 1 {
		t.Fatalf("purge while a request holds k-held = %d %v, want 1 (k-other) and no wait", deleted, err)
	}
	close(parked.release)

	a := awaitAnswer(t, "the request holding k-held", held)
	if a.status != http.StatusOK || a.hdr.Get("Idempotency-Replayed") != "" || !strings.Contains(a.body, `"taskId"`) {
		t.Fatalf("the held request = %s, want a fresh 200 with the recommendation", a)
	}
	if n := svc.count("idempotency_keys WHERE key = 'k-held'"); n != 1 {
		t.Fatalf("rows for k-held = %d, want 1: the held row was replaced, not deleted", n)
	}
	if status, body, hdr := svc.simulate(t, simulation, "k-held"); status != http.StatusOK || body != a.body || hdr.Get("Idempotency-Replayed") != "true" {
		t.Fatalf("replay of k-held = %d %s %v, want the held request's answer replayed", status, body, hdr)
	}
}

// TestPostgresRequestsOnExpiredKeysRacingThePurgeStayCorrect: for each key
// whose row is already expired, three concurrent duplicates race a purge
// pass. Every answer is the same 200 (one fresh execution, two replays of the
// winner), exactly one live row remains per key and it is what a later
// request replays, whatever order the purge and the requests committed in.
func TestPostgresRequestsOnExpiredKeysRacingThePurgeStayCorrect(t *testing.T) {
	var url string
	svc := bootPostgresOver(t, func(_ *adapters, dbURL string) { url = dbURL })
	svc.declare(t)
	for i := range 6 {
		key := fmt.Sprintf("k-race-%d", i)
		seedExpiredKey(t, url, key, 30*time.Hour)

		copies := make([]<-chan answer, 3)
		for j := range copies {
			copies[j] = simulateAsync(svc.srv.URL, simulation, key)
		}
		purged := make(chan error, 1)
		go func() {
			_, err := purgeIdempotencyKeys(context.Background(), svc.ad.idempotencyStore, svc.ad.clock, idempotencyPurgeBatch)
			purged <- err
		}()
		var bodies []string
		for _, c := range copies {
			a := awaitAnswer(t, "copy "+key, c)
			if a.status != http.StatusOK {
				t.Fatalf("copy of %s = %s, want a 200", key, a)
			}
			bodies = append(bodies, a.body)
		}
		if err := <-purged; err != nil {
			t.Fatalf("the racing purge of %s failed: %v", key, err)
		}
		for _, b := range bodies[1:] {
			if b != bodies[0] {
				t.Fatalf("the copies of %s answered different bodies", key)
			}
		}
		if status, body, hdr := svc.simulate(t, simulation, key); status != http.StatusOK || hdr.Get("Idempotency-Replayed") != "true" || body != bodies[0] {
			t.Fatalf("a later request on %s = %d %v, want the winner's answer replayed", key, status, hdr.Get("Idempotency-Replayed"))
		}
		if n := svc.count(fmt.Sprintf("idempotency_keys WHERE key = '%s'", key)); n != 1 {
			t.Fatalf("rows for %s = %d, want exactly one live row", key, n)
		}
	}
}
