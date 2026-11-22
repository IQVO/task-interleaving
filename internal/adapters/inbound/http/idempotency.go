package http

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/claudioed/task-interleaving/internal/application/ports"
	"github.com/claudioed/task-interleaving/internal/application/repository"
)

// IdempotencyKeyHeader is the request header a caller supplies on a POST or
// PATCH wrapped by Idempotency.Middleware.
const IdempotencyKeyHeader = "Idempotency-Key"

// IdempotencyReplayedHeader is the response header ("true") of every replayed
// answer, and only of a replay: the first execution never carries it.
const IdempotencyReplayedHeader = "Idempotency-Replayed"

// maxIdempotencyKeyLen is the OpenAPI maxLength of the key.
const maxIdempotencyKeyLen = 128

// IdempotencyWindow is how long a stored answer is replayed (the fleet rule,
// harness docs/idempotency-replay-classes.md): an answer older than this is
// not found by Lookup, the key is a new request again and its new answer
// replaces the expired row.
const IdempotencyWindow = 24 * time.Hour

// Control flow of the fresh path; neither escapes the middleware.
var (
	errRollback = errors.New("response is not stored: roll the request back")
	errLostRace = errors.New("another request stored an answer for this idempotency key first")
)

// Idempotency is the fleet's Idempotency-Key middleware for the POSTs of this
// API, built on the application's ports so the adapter imports no sibling
// adapter. The argument in short:
//
//   - A key that already has a stored answer replays it (same fingerprint:
//     method, path and SHA-256 of the body) with the response header
//     Idempotency-Replayed: true, or is 409 idempotency-key-reused (different
//     fingerprint). Nothing runs.
//   - A new key runs the handler INSIDE one unit of work. A 2xx answer is saved
//     in that same transaction, so the effects, their outbox rows and the
//     stored answer commit or roll back together.
//   - A refusal is handled by the replay class its problem type declares
//     (replay_class.go, ADR 0013); the middleware holds no list of slugs. FINAL
//     rolls the unit of work back (a rejected request leaves no effect, and a
//     Postgres transaction that hit a constraint is aborted anyway) and is then
//     stored on its own, so the same key and body replay the same rejection.
//     OUTCOME commits the handler's writes with the stored answer, exactly as a
//     2xx does. RETRYABLE, and a refusal with no class, rolls back and stores
//     NOTHING: the key is released and a retry executes again, and a different
//     body under that key is a new request. A 5xx answer is rolled back and
//     NEVER stored, so the same key can retry the real work.
//   - Two concurrent requests with one new key both run, but the store keeps
//     the first writer's answer (ports.IdempotencyStore.Save): after saving, a
//     request re-reads the key in its own transaction, and the one that finds
//     another answer rolls back its effects and replays the winner's. Under
//     Postgres the second Save blocks on the key's unique index until the
//     first transaction ends, so the re-read sees the committed winner.
//   - A stored answer replays for IdempotencyWindow (24 h) after it was
//     created, and not a microsecond longer.
//   - An in-flight duplicate replays, it never answers its own unstored
//     refusal: before a RETRYABLE, unclassified or 5xx answer goes out, the
//     key is re-read and a stored answer found there is replayed instead
//     (finishUnstored).
type Idempotency struct {
	Store  ports.IdempotencyStore
	UoW    ports.UnitOfWork
	Clock  ports.Clock
	Logger *slog.Logger
}

// NewIdempotency builds the middleware over the idempotency store, the unit of
// work the handlers' use cases also use, and the clock.
func NewIdempotency(store ports.IdempotencyStore, uow ports.UnitOfWork, clock ports.Clock) *Idempotency {
	return &Idempotency{Store: store, UoW: uow, Clock: clock, Logger: slog.Default()}
}

// notBefore is the oldest creation instant a replayable answer may have.
func (m *Idempotency) notBefore() time.Time { return m.Clock.Now().UTC().Add(-IdempotencyWindow) }

// Middleware wraps next.
func (m *Idempotency) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, ok := requestKey(w, r)
		if !ok {
			return
		}
		body, ok := bufferBody(w, r)
		if !ok {
			return
		}
		fp := fingerprint(r, body)
		rec, found, err := m.Store.Lookup(r.Context(), key, m.notBefore())
		if err != nil {
			writeError(w, r, err)
			return
		}
		if found {
			replay(w, r, rec, fp)
			return
		}
		m.serveFresh(w, r, next, key, fp)
	})
}

// requestKey reads the Idempotency-Key; a missing key is answered 400
// idempotency-key-required and a key that is not 1 to 128 printable ASCII
// characters 400 idempotency-key-invalid (not ok in both cases).
func requestKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := r.Header.Get(IdempotencyKeyHeader)
	if key == "" {
		writeProblem(w, r, idempotencyKeyRequired, "send an Idempotency-Key header with a unique value per logical request")
		return "", false
	}
	if !printableASCII(key) || len(key) > maxIdempotencyKeyLen {
		writeProblem(w, r, idempotencyKeyInvalid, "the Idempotency-Key header must be 1 to 128 printable ASCII characters")
		return "", false
	}
	return key, true
}

// printableASCII reports whether every byte of s is a printable ASCII character.
func printableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// bufferBody reads the body once so it can be fingerprinted and still be read
// by the handler.
func bufferBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	_ = r.Body.Close()
	if err != nil || len(body) > maxBodyBytes {
		writeError(w, r, errMalformedRequest)
		return nil, false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, true
}

// fingerprint identifies a request: the same key may only ever carry the same
// method, path and body.
func fingerprint(r *http.Request, body []byte) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s\n%s\n", r.Method, r.URL.EscapedPath())
	_, _ = h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// storedAnswer is what the record's Body holds: the response headers
// (Content-Type, ...) with the body, so a replay is byte-identical.
type storedAnswer struct {
	Header http.Header `json:"header,omitempty"`
	Body   []byte      `json:"body"`
}

// capture records the handler's response so it can be stored (and committed)
// BEFORE anything reaches the client.
type capture struct {
	header http.Header
	code   int
	body   bytes.Buffer
	class  replayClass
}

// MarkReplay records the replay class of the problem the handler is about to
// write (replayMarker): writeProblem calls it.
func (c *capture) MarkReplay(class replayClass) { c.class = class }

// keepsWrites reports whether the handler's writes commit with the stored
// answer: a 2xx, or the OUTCOME refusal of a command that committed. A 5xx never.
func (c *capture) keepsWrites() bool {
	return c.code/100 == 2 || (c.code < http.StatusInternalServerError && c.class == replayOutcome)
}

// storesRefusal reports whether a refusal whose unit of work rolled back is
// still stored on its own: only a FINAL one. A RETRYABLE, an unclassified and
// a 5xx answer release the key.
func (c *capture) storesRefusal() bool {
	return c.code < http.StatusInternalServerError && c.class == replayFinal
}

func (c *capture) Header() http.Header { return c.header }

func (c *capture) WriteHeader(code int) {
	if c.code == 0 {
		c.code = code
	}
}

func (c *capture) Write(b []byte) (int, error) {
	if c.code == 0 {
		c.code = http.StatusOK
	}
	return c.body.Write(b)
}

func (m *Idempotency) serveFresh(w http.ResponseWriter, r *http.Request, next http.Handler, key, fp string) {
	var (
		out    *capture
		winner repository.IdempotencyRecord
	)
	err := m.UoW.Do(r.Context(), func(ctx context.Context) error {
		out = serveCaptured(next, r.WithContext(ctx))
		if !out.keepsWrites() {
			return errRollback
		}
		rec, err := m.record(key, fp, out)
		if err != nil {
			return err
		}
		stored, err := m.saveAndReread(ctx, rec)
		if err != nil {
			return err
		}
		if !sameAnswer(stored, rec) {
			winner = stored
			return errLostRace
		}
		return nil
	})
	if err == nil {
		flush(w, out)
		return
	}
	if errors.Is(err, errLostRace) {
		replay(w, r, winner, fp)
		return
	}
	if errors.Is(err, errRollback) {
		m.finishRejected(w, r, key, fp, out)
		return
	}
	writeError(w, r, err)
}

// serveCaptured runs next and returns what it answered (200 when it wrote
// nothing).
func serveCaptured(next http.Handler, r *http.Request) *capture {
	out := &capture{header: http.Header{}}
	next.ServeHTTP(out, r)
	if out.code == 0 {
		out.code = http.StatusOK
	}
	return out
}

func (m *Idempotency) record(key, fp string, out *capture) (repository.IdempotencyRecord, error) {
	body, err := json.Marshal(storedAnswer{Header: out.header, Body: out.body.Bytes()})
	if err != nil {
		return repository.IdempotencyRecord{}, fmt.Errorf("encode idempotency answer: %w", err)
	}
	return repository.IdempotencyRecord{
		Key: key, Fingerprint: fp, Status: out.code, ContentType: out.header.Get("Content-Type"), Body: body,
		// Microseconds: the precision Postgres keeps, so the re-read compares equal.
		CreatedAt: m.Clock.Now().UTC().Truncate(time.Microsecond),
	}, nil
}

// saveAndReread saves rec and returns the record the store holds for its key
// afterwards: rec itself, or the answer of the request that won the race.
func (m *Idempotency) saveAndReread(ctx context.Context, rec repository.IdempotencyRecord) (repository.IdempotencyRecord, error) {
	if err := m.Store.Save(ctx, rec, m.notBefore()); err != nil {
		return repository.IdempotencyRecord{}, err
	}
	stored, found, err := m.Store.Lookup(ctx, rec.Key, m.notBefore())
	if err != nil {
		return repository.IdempotencyRecord{}, err
	}
	if !found {
		return repository.IdempotencyRecord{}, errors.New("idempotency answer vanished after it was saved")
	}
	return stored, nil
}

func sameAnswer(a, b repository.IdempotencyRecord) bool {
	return a.Fingerprint == b.Fingerprint && a.Status == b.Status &&
		bytes.Equal(a.Body, b.Body) && a.CreatedAt.Equal(b.CreatedAt)
}

// finishRejected answers a request whose unit of work was rolled back: a FINAL
// refusal is stored on its own first so a retry with the same key and body
// replays it. Every other answer (a 5xx, a RETRYABLE or unclassified refusal)
// is never stored: the key row was never written, so the key is released; it
// re-reads the key first (finishUnstored). If another request stored an answer
// for the key first (it won a race this one lost with a FINAL refusal), that
// answer is replayed instead.
func (m *Idempotency) finishRejected(w http.ResponseWriter, r *http.Request, key, fp string, out *capture) {
	if !out.storesRefusal() {
		m.finishUnstored(w, r, key, fp, out)
		return
	}
	rec, err := m.record(key, fp, out)
	if err == nil {
		err = m.Store.Save(r.Context(), rec, m.notBefore())
	}
	if err != nil {
		m.Logger.WarnContext(r.Context(), "idempotency: answer not stored", "error", logReplacer.Replace(err.Error()))
		flush(w, out)
		return
	}
	stored, found, err := m.Store.Lookup(r.Context(), key, m.notBefore())
	if err == nil && found && !sameAnswer(stored, rec) {
		replay(w, r, stored, fp)
		return
	}
	flush(w, out)
}

// finishUnstored answers an unstored refusal (RETRYABLE, unclassified or 5xx)
// after re-reading the key (fleet audit 2026-11-21, defect 1; harness
// docs/idempotency-replay-classes.md "In-flight duplicates"). The key Lookup
// runs before the unit of work and nothing holds the key while a request
// runs, so a same-key copy that arrived while the original was in flight
// passed that Lookup and decided against whatever state it then read. When
// the original's answer is stored by now (within IdempotencyWindow), the copy
// answers that stored answer instead of its own refusal: N copies are one run
// and N-1 replays (another body: idempotency-key-reused). Re-reading here is
// the same as re-reading under a lock, because the refusal already rolled back
// every write. A failed re-read is logged at WARN and the request's own
// refusal goes out (the key stays released).
func (m *Idempotency) finishUnstored(w http.ResponseWriter, r *http.Request, key, fp string, out *capture) {
	stored, found, err := m.Store.Lookup(r.Context(), key, m.notBefore())
	if err != nil {
		m.Logger.WarnContext(r.Context(), "idempotency: re-read before an unstored refusal failed", "error", logReplacer.Replace(err.Error()))
	} else if found {
		replay(w, r, stored, fp)
		return
	}
	flush(w, out)
}

// replay answers a request whose key already has a stored answer: that answer
// (marked Idempotency-Replayed: true) when the fingerprint matches, 409 when
// it does not.
func replay(w http.ResponseWriter, r *http.Request, rec repository.IdempotencyRecord, fp string) {
	if rec.Fingerprint != fp {
		writeProblem(w, r, idempotencyKeyReused, "the Idempotency-Key was used with a different request (method, path or body)")
		return
	}
	var answer storedAnswer
	if err := json.Unmarshal(rec.Body, &answer); err != nil {
		writeError(w, r, fmt.Errorf("decode stored idempotency answer: %w", err))
		return
	}
	for k, vs := range answer.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set(IdempotencyReplayedHeader, "true")
	w.WriteHeader(rec.Status)
	_, _ = w.Write(answer.Body)
}

// flush copies the captured response onto the real client connection.
func flush(w http.ResponseWriter, out *capture) {
	for k, vs := range out.header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(out.code)
	_, _ = w.Write(out.body.Bytes())
}
