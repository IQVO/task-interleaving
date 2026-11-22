//go:build integration

package main

import (
	"net/http"
	"strings"
	"testing"
)

const (
	noPosition = `{"stationId":"STN-07"}`
	blankID    = `{"stationId":" ","fromLocation":"A-01"}`
)

// expectRefusal asserts a first (never replayed) refusal with the slug.
func expectRefusal(t *testing.T, status int, body string, hdr http.Header, wantStatus int, slug string) {
	t.Helper()
	if status != wantStatus || !strings.Contains(body, "/"+slug) {
		t.Fatalf("answer = %d %s, want %d %s", status, body, wantStatus, slug)
	}
	if _, replayed := hdr["Idempotency-Replayed"]; replayed {
		t.Fatalf("a first execution carries Idempotency-Replayed: %v", hdr)
	}
}

// RETRYABLE (ADR 0011): policy-not-declared depends on the site declaring its
// policy later. The refusal rolls the unit of work back and stores nothing, so
// the key is free: after the declaration the SAME key and body execute again,
// succeed, and only then is the answer stored and replayed.
func TestPostgresARetryableRefusalIsNotStoredAndItsKeyRetries(t *testing.T) {
	svc := bootPostgres(t) // no policy declared yet
	status, body, hdr := svc.simulate(t, simulation, "k-no-policy")
	expectRefusal(t, status, body, hdr, http.StatusUnprocessableEntity, "policy-not-declared")
	for _, table := range []string{"interleaving_policies", "hints", "station_trips", "outbox_events", "idempotency_keys"} {
		if n := svc.count(table); n != 0 {
			t.Fatalf("%s has %d rows after a RETRYABLE refusal, want none (the key is released)", table, n)
		}
	}

	svc.declare(t) // the site declares its policy: the same request can pass now
	status, body, hdr = svc.simulate(t, simulation, "k-no-policy")
	if status != http.StatusOK || !strings.Contains(body, `"taskId":"task-9c2e"`) {
		t.Fatalf("retry under the released key = %d %s, want the work to run (200)", status, body)
	}
	if _, replayed := hdr["Idempotency-Replayed"]; replayed {
		t.Fatalf("the retry of a released key executes, it is not a replay: %v", hdr)
	}
	if n := svc.count("idempotency_keys"); n != 1 {
		t.Fatalf("idempotency rows = %d after the success, want 1", n)
	}
	status2, body2, hdr2 := svc.simulate(t, simulation, "k-no-policy")
	if status2 != status || body2 != body || hdr2.Get("Idempotency-Replayed") != "true" {
		t.Fatalf("the success is stored and replayed: %d %s %v", status2, body2, hdr2)
	}
}

// A second RETRYABLE: station-position-unknown is a fact the fulfillment
// consumer delivers (the station's first completion). A different body under
// the released key is a NEW request, not idempotency-key-reused.
func TestPostgresAStationPositionRefusalIsReleasedUntilTheStationCompletesATask(t *testing.T) {
	svc := bootPostgres(t)
	svc.declare(t)
	status, body, hdr := svc.simulate(t, noPosition, "k-no-position")
	expectRefusal(t, status, body, hdr, http.StatusUnprocessableEntity, "station-position-unknown")
	if n := svc.count("idempotency_keys"); n != 0 {
		t.Fatalf("idempotency rows = %d after a RETRYABLE refusal, want none", n)
	}
	if status, body, _ := svc.simulate(t, simulation, "k-no-position"); status != http.StatusOK {
		t.Fatalf("another body under the released key = %d %s, want a new request (200), not idempotency-key-reused", status, body)
	}
	if n := svc.count("idempotency_keys"); n != 1 {
		t.Fatalf("idempotency rows = %d, want the new request's answer", n)
	}

	completeTask(t, svc.ad, "task-done") // the station now has a recorded position
	status, body, hdr = svc.simulate(t, noPosition, "k-position-later")
	if status != http.StatusOK {
		t.Fatalf("after the station completed a task = %d %s, want 200", status, body)
	}
	if _, replayed := hdr["Idempotency-Replayed"]; replayed {
		t.Fatalf("a first execution carries Idempotency-Replayed: %v", hdr)
	}
}

// FINAL: a blank station id is a function of the request alone. It is stored
// (the unit of work rolled back, nothing else written) and replays with the
// header, byte for byte, even after the world changed.
func TestPostgresAFinalRefusalIsStoredAndReplayed(t *testing.T) {
	svc := bootPostgres(t)
	svc.declare(t)
	status, body, hdr := svc.simulate(t, blankID, "k-blank")
	expectRefusal(t, status, body, hdr, http.StatusBadRequest, "invalid-station-id")
	if n := svc.count("idempotency_keys"); n != 1 {
		t.Fatalf("idempotency rows = %d after a FINAL refusal, want 1", n)
	}
	status2, body2, hdr2 := svc.simulate(t, blankID, "k-blank")
	if status2 != status || body2 != body || hdr2.Get("Idempotency-Replayed") != "true" || hdr2.Get("Content-Type") != hdr.Get("Content-Type") {
		t.Fatalf("FINAL replay differs or is not marked: %d %s %v", status2, body2, hdr2)
	}
	if status, body, _ := svc.simulate(t, simulation, "k-blank"); status != http.StatusConflict || !strings.Contains(body, "idempotency-key-reused") {
		t.Fatalf("another body under a stored key = %d %s, want 409 idempotency-key-reused", status, body)
	}
	for _, table := range []string{"interleaving_policies", "hints", "station_trips"} {
		want := 0
		if table == "interleaving_policies" {
			want = 1 // the declaration, not the refusal
		}
		if n := svc.count(table); n != want {
			t.Fatalf("%s has %d rows, want %d", table, n, want)
		}
	}
}
