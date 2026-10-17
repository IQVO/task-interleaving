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
	t.Run("an unknown key is not found", func(t *testing.T) {
		if _, ok, err := newStore(t).Lookup(bg, "k"); err != nil || ok {
			t.Fatalf("Lookup = %v %v", ok, err)
		}
	})
	t.Run("save then lookup round trips the answer", func(t *testing.T) {
		s := newStore(t)
		in := repository.IdempotencyRecord{
			Key: "k1", Fingerprint: "fp", Status: 201, ContentType: "application/json",
			Body: []byte(`{"hintId":"hint-1"}`), CreatedAt: T0,
		}
		must(t, s.Save(bg, in))
		got, ok, err := s.Lookup(bg, "k1")
		must(t, err)
		if !ok || got.Key != "k1" || got.Fingerprint != "fp" || got.Status != 201 || got.ContentType != "application/json" ||
			string(got.Body) != string(in.Body) || !got.CreatedAt.Equal(T0) {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("the first writer of a key wins", func(t *testing.T) {
		s := newStore(t)
		must(t, s.Save(bg, repository.IdempotencyRecord{Key: "k", Fingerprint: "one", Status: 201, CreatedAt: T0}))
		must(t, s.Save(bg, repository.IdempotencyRecord{Key: "k", Fingerprint: "two", Status: 409, CreatedAt: T0.Add(time.Hour)}))
		got, _, err := s.Lookup(bg, "k")
		must(t, err)
		if got.Fingerprint != "one" || got.Status != 201 {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("an empty body stays empty", func(t *testing.T) {
		s := newStore(t)
		must(t, s.Save(bg, repository.IdempotencyRecord{Key: "k", Fingerprint: "fp", Status: 204, CreatedAt: T0}))
		got, ok, err := s.Lookup(bg, "k")
		must(t, err)
		if !ok || len(got.Body) != 0 || got.Status != 204 {
			t.Fatalf("got %+v %v", got, ok)
		}
	})
}
