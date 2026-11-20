package http_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/claudioed/task-interleaving/internal/application/repository"
)

const (
	idemHeader = "Idempotency-Key"
	simulate   = `{"stationId":"STN-07","fromLocation":"A-01"}`
	// blankStation is a refusal that is a function of the request alone
	// (invalid-station-id, FINAL).
	blankStation = `{"stationId":" ","fromLocation":"A-01"}`

	// replayedHeader marks a replayed answer, and only a replay.
	replayedHeader = "Idempotency-Replayed"
)

// expectFirstExecution asserts r carries no replay marker.
func expectFirstExecution(t *testing.T, r response) {
	t.Helper()
	if _, present := r.header[replayedHeader]; present {
		t.Fatalf("a first execution carries %s: %v", replayedHeader, r.header)
	}
}

// expectReplayOf asserts again is the byte-identical stored answer of first,
// marked Idempotency-Replayed: true.
func expectReplayOf(t *testing.T, first, again response) {
	t.Helper()
	expectFirstExecution(t, first)
	if got := again.header.Get(replayedHeader); got != "true" {
		t.Fatalf("replay %s = %q, want true (%v)", replayedHeader, got, again.header)
	}
	if again.status != first.status || again.raw != first.raw || again.contentType() != first.contentType() {
		t.Fatalf("replay differs:\nfirst  %d %s %v\nreplay %d %s %v", first.status, first.raw, first.header, again.status, again.raw, again.header)
	}
}

// idempotentWorld is a fixture with the middleware on, the layout seeded and
// the policy declared.
func idempotentWorld(t *testing.T) *fixture {
	t.Helper()
	f := newIdempotentFixture(t)
	f.seedWorld(t)
	f.declare(t)
	return f
}

func TestThePostRequiresAnIdempotencyKey(t *testing.T) {
	f := idempotentWorld(t)
	expectProblem(t, f.call(t, http.MethodPost, recommendPath, simulate), http.StatusBadRequest, "idempotency-key-required")
	expectProblem(t, f.call(t, http.MethodPost, recommendPath, simulate, idemHeader, strings.Repeat("k", 129)), http.StatusBadRequest, "idempotency-key-invalid")
	expectProblem(t, f.call(t, http.MethodPost, recommendPath, simulate, idemHeader, "caf\u00e9"), http.StatusBadRequest, "idempotency-key-invalid")
	f.mustCall(t, http.MethodPost, recommendPath, simulate, http.StatusOK, idemHeader, strings.Repeat("k", 128))
	if f.trips.gets.Load() != 1 {
		t.Fatalf("a refused POST ran the use case: %d station lookups", f.trips.gets.Load())
	}
}

func TestPutsAndGetsNeedNoIdempotencyKey(t *testing.T) {
	f := newIdempotentFixture(t)
	f.declare(t)
	f.mustCall(t, http.MethodGet, "/hints", "", http.StatusOK)
	f.mustCall(t, http.MethodGet, "/sites/SITE-1/policy", "", http.StatusOK)
}

// A replay returns the original answer byte for byte, even when the world has
// moved on; a new key sees the world as it is now.
func TestReplayReturnsTheSameAnswer(t *testing.T) {
	f := idempotentWorld(t)
	first := f.mustCall(t, http.MethodPost, recommendPath, simulate, http.StatusOK, idemHeader, "k-1")
	f.available(t, "task-0000", "PICK", "B-01") // a closer-by-id task arrives
	again := f.mustCall(t, http.MethodPost, recommendPath, simulate, http.StatusOK, idemHeader, "k-1")
	expectReplayOf(t, first, again)
	if again.contentType() != "application/json" {
		t.Fatalf("replay lost its content type: %v", again.header)
	}
	fresh := f.mustCall(t, http.MethodPost, recommendPath, simulate, http.StatusOK, idemHeader, "k-2")
	if fresh.raw == first.raw || candidateIDs(fresh)[0] != "task-0000" {
		t.Fatalf("a new key must see the current pool: %s", fresh.raw)
	}
	if len(f.events.Names()) != 1 { // the policy declaration only
		t.Fatalf("events = %v", f.events.Names())
	}
}

func TestTheSameKeyWithAnotherRequestIsRejected(t *testing.T) {
	f := idempotentWorld(t)
	f.mustCall(t, http.MethodPost, recommendPath, simulate, http.StatusOK, idemHeader, "k-1")
	for name, tc := range map[string]struct{ method, path, body string }{
		"other station":    {http.MethodPost, recommendPath, `{"stationId":"STN-08","fromLocation":"A-01"}`},
		"other location":   {http.MethodPost, recommendPath, `{"stationId":"STN-07","fromLocation":"B-01"}`},
		"other whitespace": {http.MethodPost, recommendPath, `{"stationId":"STN-07", "fromLocation":"A-01"}`},
		"no location":      {http.MethodPost, recommendPath, `{"stationId":"STN-07"}`},
	} {
		t.Run(name, func(t *testing.T) {
			r := f.call(t, tc.method, tc.path, tc.body, idemHeader, "k-1")
			expectProblem(t, r, http.StatusConflict, "idempotency-key-reused")
			expectFirstExecution(t, r) // a reuse is not a replay
		})
	}
}

// FINAL (ADR 0013): the answer is a function of the request alone; it is stored
// and replayed, the world moving on does not change it.
func TestAFinalRefusalIsStoredAndReplayed(t *testing.T) {
	f := idempotentWorld(t)
	first := f.call(t, http.MethodPost, recommendPath, blankStation, idemHeader, "k-final")
	expectProblem(t, first, http.StatusBadRequest, "invalid-station-id")
	again := f.call(t, http.MethodPost, recommendPath, blankStation, idemHeader, "k-final")
	expectProblem(t, again, http.StatusBadRequest, "invalid-station-id")
	expectReplayOf(t, first, again)
	expectProblem(t, f.call(t, http.MethodPost, recommendPath, simulate, idemHeader, "k-final"), http.StatusConflict, "idempotency-key-reused")
}

// RETRYABLE (ADR 0013): policy-not-declared depends on state that changes (the
// site declares its policy later). Nothing is stored, the key is released, the
// SAME request under the SAME key executes again and, once the state allows it,
// succeeds and is stored; a different body under a released key is a new request.
func TestARetryableRefusalIsNotStoredAndItsKeyRetries(t *testing.T) {
	f := newIdempotentFixture(t)
	f.seedWorld(t)
	first := f.call(t, http.MethodPost, recommendPath, simulate, idemHeader, "k-retry")
	expectProblem(t, first, http.StatusUnprocessableEntity, "policy-not-declared")
	expectFirstExecution(t, first)
	if saves := f.store.saves.Load(); saves != 0 {
		t.Fatalf("a RETRYABLE refusal was stored (%d saves)", saves)
	}
	again := f.call(t, http.MethodPost, recommendPath, simulate, idemHeader, "k-retry")
	expectProblem(t, again, http.StatusUnprocessableEntity, "policy-not-declared")
	expectFirstExecution(t, again) // executed again, not replayed
	other := f.call(t, http.MethodPost, recommendPath, `{"stationId":"STN-08","fromLocation":"A-01"}`, idemHeader, "k-retry")
	expectProblem(t, other, http.StatusUnprocessableEntity, "policy-not-declared") // a new request, not idempotency-key-reused

	f.declare(t) // the state changed: the same request passes now
	retry := f.mustCall(t, http.MethodPost, recommendPath, simulate, http.StatusOK, idemHeader, "k-retry")
	expectFirstExecution(t, retry)
	expectReplayOf(t, retry, f.mustCall(t, http.MethodPost, recommendPath, simulate, http.StatusOK, idemHeader, "k-retry"))
}

func TestA5xxAnswerIsNeverStored(t *testing.T) {
	f := idempotentWorld(t)
	f.trips.getFail.set(errDB)
	expectProblem(t, f.call(t, http.MethodPost, recommendPath, simulate, idemHeader, "k-5xx"), http.StatusInternalServerError, "internal-error")
	if saves := f.store.saves.Load(); saves != 0 {
		t.Fatalf("a 5xx answer was stored (%d saves)", saves)
	}
	f.trips.getFail.set(nil)
	f.mustCall(t, http.MethodPost, recommendPath, simulate, http.StatusOK, idemHeader, "k-5xx")
}

func TestIdempotencyStoreFailures(t *testing.T) {
	t.Run("lookup fails", func(t *testing.T) {
		f := idempotentWorld(t)
		f.store.lookupFail.set(errDB)
		expectProblem(t, f.call(t, http.MethodPost, recommendPath, simulate, idemHeader, "k"), http.StatusInternalServerError, "internal-error")
	})
	t.Run("save fails after a success", func(t *testing.T) {
		f := idempotentWorld(t)
		f.store.saveFail.set(errDB)
		expectProblem(t, f.call(t, http.MethodPost, recommendPath, simulate, idemHeader, "k"), http.StatusInternalServerError, "internal-error")
	})
	t.Run("save of a FINAL refusal fails but the client still gets it", func(t *testing.T) {
		f := idempotentWorld(t)
		f.store.saveFail.set(errDB)
		expectProblem(t, f.call(t, http.MethodPost, recommendPath, blankStation, idemHeader, "k"), http.StatusBadRequest, "invalid-station-id")
	})
}

// winnerAnswer is the stored answer of a request that won the race.
func winnerAnswer(t *testing.T, ours repository.IdempotencyRecord) repository.IdempotencyRecord {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"header": map[string][]string{"Content-Type": {"application/json"}},
		"body":   []byte(`{"winner":true}`),
	})
	must(t, err)
	return repository.IdempotencyRecord{
		Key: ours.Key, Fingerprint: ours.Fingerprint, Status: http.StatusOK, ContentType: "application/json",
		Body: body, CreatedAt: ours.CreatedAt.Add(time.Second),
	}
}

func TestTheLoserOfARaceReplaysTheWinnersAnswer(t *testing.T) {
	f := idempotentWorld(t)
	f.store.beforeSave = func(rec repository.IdempotencyRecord) {
		must(t, f.store.IdempotencyStore.Save(t.Context(), winnerAnswer(t, rec)))
	}
	r := f.mustCall(t, http.MethodPost, recommendPath, simulate, http.StatusOK, idemHeader, "k-race")
	if r.body["winner"] != true || r.header.Get(replayedHeader) != "true" {
		t.Fatalf("the loser did not replay the winner (marked as a replay): %s %v", r.raw, r.header)
	}
}

func TestTheLoserOfARaceWithAFinalRefusalReplaysTheWinnersAnswer(t *testing.T) {
	f := idempotentWorld(t)
	f.store.beforeSave = func(rec repository.IdempotencyRecord) {
		must(t, f.store.IdempotencyStore.Save(t.Context(), winnerAnswer(t, rec)))
	}
	r := f.mustCall(t, http.MethodPost, recommendPath, blankStation, http.StatusOK, idemHeader, "k-race4")
	if r.body["winner"] != true {
		t.Fatalf("the 4xx loser did not replay the winner: %s", r.raw)
	}
}

// Eight requests with one new key: every caller gets the same answer, exactly
// one answer is stored for the key, and the simulation wrote nothing else.
func TestConcurrentRequestsWithOneKeyShareOneAnswer(t *testing.T) {
	f := idempotentWorld(t)
	var ok, other atomic.Int32
	answers := make([]string, 8)
	parallel(8, func(i int) {
		r := f.call(t, http.MethodPost, recommendPath, simulate, idemHeader, "k-same")
		answers[i] = r.raw
		if r.status == http.StatusOK {
			ok.Add(1)
			return
		}
		other.Add(1)
	})
	if ok.Load() != 8 || other.Load() != 0 {
		t.Fatalf("ok=%d other=%d, want 8 and 0", ok.Load(), other.Load())
	}
	for i, a := range answers {
		if a != answers[0] {
			t.Fatalf("answer %d differs: %s vs %s", i, a, answers[0])
		}
	}
	stored, found, err := f.store.IdempotencyStore.Lookup(t.Context(), "k-same")
	must(t, err)
	if !found || stored.Status != http.StatusOK {
		t.Fatalf("stored = %+v found=%v", stored, found)
	}
	if n := len(f.events.Names()); n != 1 {
		t.Fatalf("events = %v: the simulation must enqueue nothing", f.events.Names())
	}
}

func TestOversizedBodyIsMalformed(t *testing.T) {
	f := idempotentWorld(t)
	big := `{"stationId":"` + strings.Repeat("a", 70<<10) + `"}`
	expectProblem(t, f.call(t, http.MethodPost, recommendPath, big, idemHeader, "k-big"), http.StatusBadRequest, "malformed-request")
}
