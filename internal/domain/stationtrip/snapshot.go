package stationtrip

import (
	"errors"
	"strings"
	"time"

	"github.com/claudioed/task-interleaving/internal/domain/recommend"
)

// ErrInvalidSnapshot is returned by Restore for a persisted state no sequence of
// transitions could have produced.
var ErrInvalidSnapshot = errors.New("stationtrip: invalid snapshot")

// Snapshot is the persisted state of a StationTrip: what a repository stores
// and hands back to Restore. Hint is nil until a hint was issued; it holds the
// most recent hint in any state. A zero LastAt means no completion was recorded.
type Snapshot struct {
	ID       StationID
	Position Position
	LastTask recommend.TaskID
	LastAt   time.Time
	Hint     *Hint
	Version  int64
}

// Snapshot returns a deep copy of the aggregate's state, without its pending
// domain events.
func (s *StationTrip) Snapshot() Snapshot {
	snap := Snapshot{
		ID:       s.id,
		Position: Position{Site: s.position.Site, Place: copyPlace(s.position.Place)},
		LastTask: s.lastTask,
		LastAt:   s.lastAt,
		Version:  s.version,
	}
	if s.hint != nil {
		h := s.hint.clone()
		snap.Hint = &h
	}
	return snap
}

// Restore rebuilds a persisted trip, validating every invariant a live trip
// holds, so a corrupt row can never become a live aggregate. The restored trip
// has no pending domain events.
func Restore(snap Snapshot) (*StationTrip, error) {
	if _, err := NewStationID(string(snap.ID)); err != nil {
		return nil, err
	}
	if err := validateSnapshotTrip(snap); err != nil {
		return nil, err
	}
	trip := &StationTrip{
		id:       snap.ID,
		position: Position{Site: snap.Position.Site, Place: copyPlace(snap.Position.Place)},
		lastTask: snap.LastTask,
		lastAt:   snap.LastAt.UTC(),
		version:  snap.Version,
	}
	if snap.Hint != nil {
		h := snap.Hint.clone()
		h.IssuedAt = h.IssuedAt.UTC()
		h.ExpiresAt = h.ExpiresAt.UTC()
		h.ConsumedAt = utcOrZero(h.ConsumedAt)
		trip.hint = &h
	}
	return trip, nil
}

func utcOrZero(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}
	return t.UTC()
}

func validateSnapshotTrip(snap Snapshot) error {
	if snap.Version < 0 {
		return ErrInvalidSnapshot
	}
	if snap.LastAt.IsZero() {
		return validateEmptyTrip(snap)
	}
	if err := validateCompletion(snap.LastTask, snap.LastAt, snap.Position); err != nil {
		return err
	}
	if snap.Version < 1 {
		return ErrInvalidSnapshot
	}
	if snap.Hint == nil {
		return nil
	}
	return validateSnapshotHint(*snap.Hint)
}

// validateEmptyTrip checks a trip that recorded nothing: no task, no hint and
// version 0.
func validateEmptyTrip(snap Snapshot) error {
	if snap.LastTask != "" || snap.Hint != nil || snap.Version != 0 {
		return ErrInvalidSnapshot
	}
	return nil
}

func validateSnapshotHint(h Hint) error {
	if _, err := NewHintID(string(h.ID)); err != nil {
		return err
	}
	if !h.State.Valid() {
		return ErrInvalidSnapshot
	}
	if strings.TrimSpace(string(h.AfterTask)) == "" || strings.TrimSpace(h.Origin) == "" {
		return ErrInvalidSnapshot
	}
	if h.IssuedAt.IsZero() || !h.ExpiresAt.After(h.IssuedAt) || h.PolicyVersion < 1 {
		return ErrInvalidSnapshot
	}
	if err := validateSnapshotCandidates(h.Candidates); err != nil {
		return err
	}
	return validateConsumption(h)
}

func validateSnapshotCandidates(cs []recommend.Candidate) error {
	if len(cs) == 0 {
		return ErrInvalidSnapshot
	}
	seen := make(map[recommend.TaskID]struct{}, len(cs))
	for i, c := range cs {
		if c.Rank != i+1 || strings.TrimSpace(string(c.TaskID)) == "" {
			return ErrInvalidSnapshot
		}
		if _, dup := seen[c.TaskID]; dup {
			return ErrInvalidSnapshot
		}
		seen[c.TaskID] = struct{}{}
	}
	return nil
}

// validateConsumption checks that the consumed task and instant are set exactly
// when the hint is Consumed, and that the task is one of its candidates.
func validateConsumption(h Hint) error {
	if h.State != StateConsumed {
		if h.ConsumedTask != "" || !h.ConsumedAt.IsZero() {
			return ErrInvalidSnapshot
		}
		return nil
	}
	if h.ConsumedAt.IsZero() {
		return ErrInvalidSnapshot
	}
	if _, ok := findCandidate(h.Candidates, h.ConsumedTask); !ok {
		return ErrInvalidSnapshot
	}
	return nil
}
