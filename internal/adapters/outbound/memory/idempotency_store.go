package memory

import (
	"context"
	"sync"

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

// Lookup implements ports.IdempotencyStore.
func (s *IdempotencyStore) Lookup(_ context.Context, key string) (repository.IdempotencyRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.rows[key]
	return rec, ok, nil
}

// Save implements ports.IdempotencyStore; the first writer of a key wins.
func (s *IdempotencyStore) Save(_ context.Context, rec repository.IdempotencyRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.rows[rec.Key]; !exists {
		s.rows[rec.Key] = rec
	}
	return nil
}
