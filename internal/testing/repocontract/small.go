package repocontract

import (
	"testing"
	"time"

	"github.com/claudioed/task-interleaving/internal/application/ports"
	"github.com/claudioed/task-interleaving/internal/application/repository"
)

// ProcessedEvents runs the ProcessedEvents contract over an EMPTY store.
func ProcessedEvents(t *testing.T, newStore func(t *testing.T) ports.ProcessedEvents) {
	t.Helper()
	t.Run("an id is claimed once per consumer", func(t *testing.T) {
		p := newStore(t)
		first, err := p.Claim(bg, "fulfillment", "e1")
		must(t, err)
		again, err := p.Claim(bg, "fulfillment", "e1")
		must(t, err)
		other, err := p.Claim(bg, "layout", "e1")
		must(t, err)
		next, err := p.Claim(bg, "fulfillment", "e2")
		must(t, err)
		if !first || again || !other || !next {
			t.Fatalf("first=%v again=%v otherConsumer=%v otherID=%v", first, again, other, next)
		}
	})
}

// IdempotencyStore runs the IdempotencyStore contract over an EMPTY store.
func IdempotencyStore(t *testing.T, newStore func(t *testing.T) ports.IdempotencyStore) {
	t.Helper()
	// live is a notBefore that keeps every answer of the cases below (all
	// created at T0 or later) inside the replay window.
	live := T0.Add(-24 * time.Hour)
	t.Run("an unknown key is not found", func(t *testing.T) {
		if _, ok, err := newStore(t).Lookup(bg, "k", live); err != nil || ok {
			t.Fatalf("Lookup = %v %v", ok, err)
		}
	})
	t.Run("save then lookup round trips the answer", func(t *testing.T) {
		s := newStore(t)
		in := repository.IdempotencyRecord{
			Key: "k1", Fingerprint: "fp", Status: 201, ContentType: "application/json",
			Body: []byte(`{"hintId":hintOne}`), CreatedAt: T0,
		}
		must(t, s.Save(bg, in, live))
		got, ok, err := s.Lookup(bg, "k1", live)
		must(t, err)
		if !ok || got.Key != "k1" || got.Fingerprint != "fp" || got.Status != 201 || got.ContentType != "application/json" ||
			string(got.Body) != string(in.Body) || !got.CreatedAt.Equal(T0) {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("the first writer of a key wins", func(t *testing.T) {
		s := newStore(t)
		must(t, s.Save(bg, repository.IdempotencyRecord{Key: "k", Fingerprint: "one", Status: 201, CreatedAt: T0}, live))
		must(t, s.Save(bg, repository.IdempotencyRecord{Key: "k", Fingerprint: "two", Status: 409, CreatedAt: T0.Add(time.Hour)}, live))
		got, _, err := s.Lookup(bg, "k", live)
		must(t, err)
		if got.Fingerprint != "one" || got.Status != 201 {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("an empty body stays empty", func(t *testing.T) {
		s := newStore(t)
		must(t, s.Save(bg, repository.IdempotencyRecord{Key: "k", Fingerprint: "fp", Status: 204, CreatedAt: T0}, live))
		got, ok, err := s.Lookup(bg, "k", live)
		must(t, err)
		if !ok || len(got.Body) != 0 || got.Status != 204 {
			t.Fatalf("got %+v %v", got, ok)
		}
	})
	idempotencyWindow(t, newStore, live)
}

// idempotencyWindow is the replay-window half of the IdempotencyStore contract:
// notBefore bounds what Lookup finds and lets Save replace an expired row.
func idempotencyWindow(t *testing.T, newStore func(t *testing.T) ports.IdempotencyStore, live time.Time) {
	t.Helper()
	const first, second = "first", "second"
	t.Run("an answer created before notBefore is not found", func(t *testing.T) {
		s := newStore(t)
		must(t, s.Save(bg, repository.IdempotencyRecord{Key: "k", Fingerprint: "fp", Status: 200, CreatedAt: T0}, live))
		if _, ok, err := s.Lookup(bg, "k", T0); err != nil || !ok {
			t.Fatalf("Lookup at the edge (notBefore = created_at) = %v %v, want found", ok, err)
		}
		if _, ok, err := s.Lookup(bg, "k", T0.Add(time.Microsecond)); err != nil || ok {
			t.Fatalf("Lookup a microsecond past the window = %v %v, want not found", ok, err)
		}
	})
	t.Run("an expired answer is replaced, a live one is kept", func(t *testing.T) {
		s := newStore(t)
		must(t, s.Save(bg, repository.IdempotencyRecord{Key: "k", Fingerprint: first, Status: 200, CreatedAt: T0}, live))
		later := T0.Add(25 * time.Hour)
		must(t, s.Save(bg, repository.IdempotencyRecord{Key: "k", Fingerprint: second, Status: 200, CreatedAt: later}, T0))
		if got, _, err := s.Lookup(bg, "k", live); err != nil || got.Fingerprint != first {
			t.Fatalf("a row at exactly notBefore is live and kept: got %+v %v", got, err)
		}
		must(t, s.Save(bg, repository.IdempotencyRecord{Key: "k", Fingerprint: second, Status: 201, CreatedAt: later}, T0.Add(time.Microsecond)))
		got, ok, err := s.Lookup(bg, "k", T0.Add(time.Microsecond))
		must(t, err)
		if !ok || got.Fingerprint != second || got.Status != 201 || !got.CreatedAt.Equal(later) {
			t.Fatalf("an expired row is replaced by the new answer: got %+v %v", got, ok)
		}
	})
}
