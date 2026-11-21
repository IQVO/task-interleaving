package memory

import (
	"context"
	"sync"
	"time"

	"github.com/claudioed/task-interleaving/internal/application/repository"
)

// IdempotencyStore is the in-memory ports.IdempotencyStore.
type IdempotencyStore struct {
	mu   sync.Mutex
	rows map[string]repository.IdempotencyRecord
}

// NewIdempotencyStore constructs an empty IdempotencyStore.
func NewIdempotencyStore() *IdempotencyStore {
	return &IdempotencyStore{rows: map[string]repository.IdempotencyRecord{}}
}

// Lookup implements ports.IdempotencyStore: an answer created before
// notBefore is outside the replay window and not found.
func (s *IdempotencyStore) Lookup(_ context.Context, key string, notBefore time.Time) (repository.IdempotencyRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.rows[key]
	if !ok || rec.CreatedAt.Before(notBefore) {
		return repository.IdempotencyRecord{}, false, nil
	}
	return rec, true, nil
}

// Save implements ports.IdempotencyStore; the first writer of a key wins, and
// an answer created before notBefore is replaced.
func (s *IdempotencyStore) Save(_ context.Context, rec repository.IdempotencyRecord, notBefore time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if prev, exists := s.rows[rec.Key]; !exists || prev.CreatedAt.Before(notBefore) {
		s.rows[rec.Key] = rec
	}
	return nil
}
