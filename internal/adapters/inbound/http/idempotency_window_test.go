package http_test

import (
	"net/http"
	"testing"
	"time"
)

// replayWindow is the fleet's replay window (harness
// docs/idempotency-replay-classes.md, "Retention must actually be enforced"):
// a stored answer replays for 24 h and not a microsecond longer.
const replayWindow = 24 * time.Hour

// carry (fleet audit 2026-11-21; ADR 0013 follow-up "no age check applies on
// replay"): a stored answer replays while it is at most 24 h old. Exactly 24 h
// later the same key and body still replay the stored answer; a microsecond
// later the key is a new request: it executes against the world as it is now,
// its answer replaces the expired one under the same key, and that NEW answer
// is what replays from then on.
func TestAStoredAnswerReplaysOnlyWithinThe24HourWindow(t *testing.T) {
	f := idempotentWorld(t)
	first := f.mustCall(t, http.MethodPost, recommendPath, simulate, http.StatusOK, idemHeader, "k-window")
	f.available(t, "task-0000", "PICK", "B-01") // a re-execution would rank this task first

	f.clock.Advance(replayWindow)
	edge := f.mustCall(t, http.MethodPost, recommendPath, simulate, http.StatusOK, idemHeader, "k-window")
	expectReplayOf(t, first, edge)

	f.clock.Advance(time.Microsecond)
	late := f.mustCall(t, http.MethodPost, recommendPath, simulate, http.StatusOK, idemHeader, "k-window")
	expectFirstExecution(t, late)
	if late.raw == first.raw || candidateIDs(late)[0] != "task-0000" {
		t.Fatalf("a key older than 24 h must execute against the current pool: %s", late.raw)
	}
	again := f.mustCall(t, http.MethodPost, recommendPath, simulate, http.StatusOK, idemHeader, "k-window")
	expectReplayOf(t, late, again)
}

// The window also frees the key for another body: past 24 h the same key with
// another request is a new request, not idempotency-key-reused.
func TestAnExpiredKeyWithAnotherBodyIsANewRequest(t *testing.T) {
	f := idempotentWorld(t)
	f.mustCall(t, http.MethodPost, recommendPath, simulate, http.StatusOK, idemHeader, "k-expired")
	f.clock.Advance(replayWindow + time.Microsecond)
	other := f.mustCall(t, http.MethodPost, recommendPath, `{"stationId":"STN-08","fromLocation":"A-01"}`, http.StatusOK, idemHeader, "k-expired")
	expectFirstExecution(t, other)
}
