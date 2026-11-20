//go:build !integration

package http_test

// The repo-specific half of the replay-class template (idempotency_replay_classes_test.go,
// rendered verbatim from warehouse-harness-template): the REAL middleware over the in-memory
// ports, whose unit of work rolls every participant back on error exactly as the Postgres one
// does. The Postgres twin, run by the integration job, is
// idempotency_replay_classes_wiring_integration_test.go.

import (
	"context"
	"net/http"
	"sync"
	"testing"

	inboundhttp "github.com/claudioed/task-interleaving/internal/adapters/inbound/http"
	"github.com/claudioed/task-interleaving/internal/adapters/outbound/memory"
)

// probeRows is the scratch table of the write the fake handler makes; it joins
// the unit of work as a participant, so a rollback removes its rows.
type probeRows struct {
	mu   sync.Mutex
	tags map[string]int
}

func (p *probeRows) add(tag string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tags[tag]++
}

func (p *probeRows) count(tag string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.tags[tag]
}

// Snapshot implements memory.Snapshotter.
func (p *probeRows) Snapshot() func() {
	p.mu.Lock()
	defer p.mu.Unlock()
	saved := make(map[string]int, len(p.tags))
	for k, v := range p.tags {
		saved[k] = v
	}
	return func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.tags = saved
	}
}

// replayCatalogue is the production catalogue plus the synthetic OUTCOME probe.
func replayCatalogue() []problemEntry {
	entries := []problemEntry{{Slug: inboundhttp.OutcomeProbeSlug, Status: http.StatusConflict, Class: classOutcome}}
	for _, e := range inboundhttp.ReplayCatalogue() {
		entries = append(entries, problemEntry{Slug: e.Slug, Status: e.Status, Class: e.Class})
	}
	return entries
}

func newReplayOps(t *testing.T) replayOps {
	t.Helper()
	probe := &probeRows{tags: map[string]int{}}
	store := memory.NewIdempotencyStore()
	mw := inboundhttp.NewIdempotency(store, memory.NewUnitOfWork(probe), newClock())
	return replayOps{
		Catalogue:  replayCatalogue,
		Middleware: func(_ *testing.T, next http.Handler) http.Handler { return mw.Middleware(next) },
		Refuse: func(w http.ResponseWriter, r *http.Request, e problemEntry) {
			inboundhttp.RefuseForTest(w, r, e.Slug)
		},
		Write:     func(_ context.Context, tag string) error { probe.add(tag); return nil },
		Committed: func(_ *testing.T, tag string) int { return probe.count(tag) },
		KeyRows: func(t *testing.T, key string) int {
			_, found, err := store.Lookup(t.Context(), key)
			must(t, err)
			if found {
				return 1
			}
			return 0
		},
	}
}
