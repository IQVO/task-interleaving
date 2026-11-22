package memory

import (
	"context"
	"slices"
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

// PurgeExpired implements ports.IdempotencyStore: the answers created before
// notBefore are deleted oldest first, at most limit of them, and the count is
// returned (ADR 0013, Amendment 2).
func (s *IdempotencyStore) PurgeExpired(_ context.Context, notBefore time.Time, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	expired := make([]string, 0, len(s.rows))
	for key, rec := range s.rows {
		if rec.CreatedAt.Before(notBefore) {
			expired = append(expired, key)
		}
	}
	slices.SortFunc(expired, func(a, b string) int { return s.rows[a].CreatedAt.Compare(s.rows[b].CreatedAt) })
	if len(expired) > limit {
		expired = expired[:limit]
	}
	for _, key := range expired {
		delete(s.rows, key)
	}
	return len(expired), nil
}
