package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	inboundhttp "github.com/claudioed/task-interleaving/internal/adapters/inbound/http"
	"github.com/claudioed/task-interleaving/internal/adapters/outbound/memory"
	"github.com/claudioed/task-interleaving/internal/application/repository"
)

// blindStore stands for the key Lookup an in-flight duplicate made BEFORE the
// original committed: while blind > 0 a Lookup reports "not found" (and counts
// down); every later one reads the store. failAfter > 0 makes every Lookup
// after the first failAfter ones fail, the way a database that dropped between
// the request and the refusal's re-read does.
type blindStore struct {
	*memory.IdempotencyStore
	mu        sync.Mutex
	blind     int
	failAfter int
	lookups   int
}

func (s *blindStore) Lookup(ctx context.Context, key string, notBefore time.Time) (repository.IdempotencyRecord, bool, error) {
	s.mu.Lock()
	s.lookups++
	n, blind := s.lookups, s.blind > 0
	if blind {
		s.blind--
	}
	s.mu.Unlock()
	if s.failAfter > 0 && n > s.failAfter {
		return repository.IdempotencyRecord{}, false, errors.New("idempotency store unreachable")
	}
	if blind {
		return repository.IdempotencyRecord{}, false, nil
	}
	return s.IdempotencyStore.Lookup(ctx, key, notBefore)
}

// scriptedHandler answers its i-th call with script[i] (the last entry repeats).
type scriptedHandler struct {
	script []http.HandlerFunc
	calls  int
}

func (h *scriptedHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	i := min(h.calls, len(h.script)-1)
	h.calls++
	h.script[i](w, r)
}

type inflightRig struct {
	store *blindStore
	clock *testClock
	h     *scriptedHandler
	mw    http.Handler
	logs  *bytes.Buffer
}

func newInflightRig(script ...http.HandlerFunc) *inflightRig {
	r := &inflightRig{store: &blindStore{IdempotencyStore: memory.NewIdempotencyStore()}, clock: newClock(),
		h: &scriptedHandler{script: script}, logs: &bytes.Buffer{}}
	m := inboundhttp.NewIdempotency(r.store, memory.NewUnitOfWork(), r.clock)
	m.Logger = slog.New(slog.NewJSONHandler(r.logs, nil))
	r.mw = m.Middleware(r.h)
	return r
}

const recommended = `{"stationId":"STN-07","candidates":[{"taskId":"task-9c2e"}]}`

func recommending(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, recommended)
}

func refusingWith(slug string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { inboundhttp.RefuseForTest(w, r, slug) }
}

func slugOf(body string) string {
	var p struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal([]byte(body), &p)
	return p.Type[strings.LastIndex(p.Type, "/")+1:]
}

// carry (fleet audit 2026-11-21, defect 1 / P3-R15b; asrs-control #16 shape):
// a duplicate that passed the key Lookup while the original was in flight (the
// blind Lookup stands for it) and then decided a RETRYABLE refusal against the
// state the original committed answers the stored 200 as a replay, not its own
// 422. A 5xx it reaches the same way replays too, and a copy with another body
// answers 409 idempotency-key-reused. The Postgres probe through the real
// router is cmd/api TestPostgresAnInFlightDuplicateReplaysTheOriginalsAnswer.
func TestAnInFlightDuplicateThatDecidesAnUnstoredRefusalReplaysTheStoredAnswer(t *testing.T) {
	r := newInflightRig(recommending, refusingWith("policy-not-declared"))
	if first := post(t, r.mw, "k-dup", simulate); first.Code != http.StatusOK || replayed(first) {
		t.Fatalf("first = %d replayed=%v %s, want a fresh 200", first.Code, replayed(first), first.Body)
	}
	r.store.blind = 1
	dup := post(t, r.mw, "k-dup", simulate)
	if dup.Code != http.StatusOK || !replayed(dup) || dup.Body.String() != recommended {
		t.Fatalf("in-flight duplicate = %d replayed=%v %s, want the first 200 replayed", dup.Code, replayed(dup), dup.Body)
	}
	r.h.script = append(r.h.script, refusingWith("internal-error"))
	r.store.blind = 1
	if five := post(t, r.mw, "k-dup", simulate); five.Code != http.StatusOK || !replayed(five) {
		t.Fatalf("in-flight duplicate answering a 5xx = %d replayed=%v %s, want the first 200 replayed", five.Code, replayed(five), five.Body)
	}
	r.store.blind = 1
	other := post(t, r.mw, "k-dup", `{"stationId":"STN-08","fromLocation":"A-01"}`)
	if other.Code != http.StatusConflict || slugOf(other.Body.String()) != keyReusedSlug {
		t.Fatalf("in-flight copy with another body = %d %s, want 409 %s", other.Code, other.Body, keyReusedSlug)
	}
	if r.h.calls != 4 {
		t.Fatalf("handler calls = %d, want 4 (every blind copy ran its use case)", r.h.calls)
	}
}

// carry (fleet audit 2026-11-21): the re-read honours the 24 h replay window.
// A stored answer exactly 24 h old is still replayed; one a microsecond older
// is outside the window, so the duplicate's own RETRYABLE refusal goes out
// (and the key stays released).
func TestTheReReadBeforeAnUnstoredRefusalHonoursThe24HourWindow(t *testing.T) {
	r := newInflightRig(recommending, refusingWith("policy-not-declared"))
	if first := post(t, r.mw, "k-win", simulate); first.Code != http.StatusOK {
		t.Fatalf("first = %d %s", first.Code, first.Body)
	}
	r.clock.Advance(inboundhttp.IdempotencyWindow)
	r.store.blind = 1
	if edge := post(t, r.mw, "k-win", simulate); edge.Code != http.StatusOK || !replayed(edge) {
		t.Fatalf("duplicate at exactly 24 h = %d replayed=%v %s, want the stored 200 replayed", edge.Code, replayed(edge), edge.Body)
	}
	r.clock.Advance(time.Microsecond)
	r.store.blind = 1
	late := post(t, r.mw, "k-win", simulate)
	if late.Code != http.StatusUnprocessableEntity || replayed(late) || slugOf(late.Body.String()) != "policy-not-declared" {
		t.Fatalf("duplicate past 24 h = %d replayed=%v %s, want its own 422 policy-not-declared", late.Code, replayed(late), late.Body)
	}
}

// carry (fleet audit 2026-11-21, mfc f4b80e1): a failed re-read before an
// unstored refusal is logged at WARN, and the request's own refusal still goes
// out (the key stays released).
func TestAFailedReReadBeforeAnUnstoredRefusalIsLoggedAndTheRefusalAnswered(t *testing.T) {
	r := newInflightRig(refusingWith("policy-not-declared"))
	r.store.failAfter = 1
	rec := post(t, r.mw, "k-warn", simulate)
	if rec.Code != http.StatusUnprocessableEntity || replayed(rec) || slugOf(rec.Body.String()) != "policy-not-declared" {
		t.Fatalf("answer = %d replayed=%v %s, want the request's own 422 policy-not-declared", rec.Code, replayed(rec), rec.Body)
	}
	if r.store.lookups != 2 {
		t.Fatalf("lookups = %d, want the first Lookup and the re-read", r.store.lookups)
	}
	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(r.logs.Bytes()), &entry); err != nil {
		t.Fatalf("log = %q, want one JSON line: %v", r.logs.String(), err)
	}
	msg, _ := entry["error"].(string)
	if entry["level"] != "WARN" || entry["msg"] != "idempotency: re-read before an unstored refusal failed" ||
		!strings.Contains(msg, "idempotency store unreachable") {
		t.Fatalf("log entry = %v", entry)
	}
}
