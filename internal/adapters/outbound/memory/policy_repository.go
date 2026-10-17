package memory

import (
	"context"
	"maps"
	"sync"

	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/domain/policy"
)

// PolicyRepo is the in-memory ports.PolicyRepo. policy.Policy is an immutable
// value, so records are safe to share.
type PolicyRepo struct {
	mu   sync.Mutex
	rows map[policy.SiteID]repository.PolicyRecord
}

// NewPolicyRepo constructs an empty PolicyRepo.
func NewPolicyRepo() *PolicyRepo {
	return &PolicyRepo{rows: map[policy.SiteID]repository.PolicyRecord{}}
}

// Get implements ports.PolicyRepo.
func (r *PolicyRepo) Get(_ context.Context, site policy.SiteID) (repository.PolicyRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.rows[site]
	if !ok {
		return repository.PolicyRecord{}, repository.ErrPolicyNotFound
	}
	return rec, nil
}

// Save implements ports.PolicyRepo.
func (r *PolicyRepo) Save(_ context.Context, rec repository.PolicyRecord, loadedVersion int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	site := rec.Policy.SiteID()
	cur, exists := r.rows[site]
	if loadedVersion == 0 && exists {
		return repository.ErrConcurrentModification
	}
	if loadedVersion != 0 && (!exists || cur.Policy.Version() != loadedVersion) {
		return repository.ErrConcurrentModification
	}
	r.rows[site] = rec
	return nil
}

// Snapshot implements Snapshotter.
func (r *PolicyRepo) Snapshot() func() {
	r.mu.Lock()
	defer r.mu.Unlock()
	saved := maps.Clone(r.rows)
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.rows = saved
	}
}
