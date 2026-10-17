package memory

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
)

// StationTripRepo is the in-memory ports.StationTripRepo. It stores snapshots,
// so a caller never shares a live aggregate with the store, and keeps the hint
// history the way the Postgres adapter keeps its hints table.
type StationTripRepo struct {
	mu    sync.Mutex
	trips map[stationtrip.StationID]stationtrip.Snapshot
	hints map[stationtrip.HintID]repository.HintRecord
}

// NewStationTripRepo constructs an empty StationTripRepo.
func NewStationTripRepo() *StationTripRepo {
	return &StationTripRepo{
		trips: map[stationtrip.StationID]stationtrip.Snapshot{},
		hints: map[stationtrip.HintID]repository.HintRecord{},
	}
}

// Get implements ports.StationTripRepo.
func (r *StationTripRepo) Get(_ context.Context, id stationtrip.StationID) (*stationtrip.StationTrip, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	snap, ok := r.trips[id]
	if !ok {
		return nil, repository.ErrStationNotFound
	}
	return stationtrip.Restore(snap)
}

// Save implements ports.StationTripRepo.
func (r *StationTripRepo) Save(_ context.Context, trip *stationtrip.StationTrip, loadedVersion int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	snap := trip.Snapshot()
	cur, exists := r.trips[snap.ID]
	if loadedVersion == 0 && exists {
		return repository.ErrConcurrentModification
	}
	if loadedVersion != 0 && (!exists || cur.Version != loadedVersion) {
		return repository.ErrConcurrentModification
	}
	r.trips[snap.ID] = snap
	if snap.Hint != nil {
		r.recordHint(snap)
	}
	return nil
}

// recordHint keeps the hint history in step with the trip's current hint: a
// hint seen for the first time supersedes the station's Active one; a known
// hint that changed state moves on.
func (r *StationTripRepo) recordHint(snap stationtrip.Snapshot) {
	h := cloneHint(*snap.Hint)
	known, ok := r.hints[h.ID]
	if ok {
		if known.Hint.State != h.State {
			known.Hint = h
			known.Version++
			r.hints[h.ID] = known
		}
		return
	}
	for id, rec := range r.hints {
		if rec.StationID != snap.ID || rec.Hint.State != stationtrip.StateActive {
			continue
		}
		rec.Hint.State = stationtrip.StateSuperseded
		rec.Version++
		rec.SupersededAt = h.IssuedAt
		r.hints[id] = rec
	}
	r.hints[h.ID] = repository.HintRecord{StationID: snap.ID, SiteID: snap.Position.Site, Hint: h, Version: 1}
}

// ExpiredActive implements ports.StationTripRepo.
func (r *StationTripRepo) ExpiredActive(_ context.Context, now time.Time, limit int) ([]stationtrip.StationID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var ids []stationtrip.StationID
	for id, snap := range r.trips {
		h := snap.Hint
		if h != nil && h.State == stationtrip.StateActive && !h.ExpiresAt.After(now) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	if len(ids) > limit {
		ids = ids[:limit]
	}
	return ids, nil
}

// GetHint implements ports.StationTripRepo.
func (r *StationTripRepo) GetHint(_ context.Context, id stationtrip.HintID) (repository.HintRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.hints[id]
	if !ok {
		return repository.HintRecord{}, repository.ErrHintNotFound
	}
	rec.Hint = cloneHint(rec.Hint)
	return rec, nil
}

// ListHints implements ports.StationTripRepo.
func (r *StationTripRepo) ListHints(_ context.Context, f repository.HintFilter, after stationtrip.HintID, limit int) ([]repository.HintRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var recs []repository.HintRecord
	for _, rec := range r.hints {
		if hintMatches(rec, f) {
			rec.Hint = cloneHint(rec.Hint)
			recs = append(recs, rec)
		}
	}
	// newest first: issued-at desc, id desc
	slices.SortFunc(recs, func(a, b repository.HintRecord) int {
		return cmp.Or(b.Hint.IssuedAt.Compare(a.Hint.IssuedAt), strings.Compare(string(b.Hint.ID), string(a.Hint.ID)))
	})
	if after != "" {
		i := slices.IndexFunc(recs, func(rec repository.HintRecord) bool { return rec.Hint.ID == after })
		if i < 0 {
			return nil, nil
		}
		recs = recs[i+1:]
	}
	if len(recs) > limit {
		recs = recs[:limit]
	}
	return recs, nil
}

func hintMatches(rec repository.HintRecord, f repository.HintFilter) bool {
	if f.StationID != "" && rec.StationID != f.StationID {
		return false
	}
	if f.SiteID != "" && rec.SiteID != f.SiteID {
		return false
	}
	return f.State == "" || rec.Hint.State == f.State
}

func cloneHint(h stationtrip.Hint) stationtrip.Hint {
	h.Candidates = slices.Clone(h.Candidates)
	return h
}

// Snapshot implements Snapshotter. Stored snapshots and records are replaced,
// never mutated in place, so a shallow copy of the maps is a faithful restore
// point.
func (r *StationTripRepo) Snapshot() func() {
	r.mu.Lock()
	defer r.mu.Unlock()
	trips := maps.Clone(r.trips)
	hints := maps.Clone(r.hints)
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.trips, r.hints = trips, hints
	}
}
