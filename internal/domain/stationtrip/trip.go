package stationtrip

import (
	"cmp"
	"math"
	"strings"
	"time"

	"github.com/claudioed/task-interleaving/internal/domain/policy"
	"github.com/claudioed/task-interleaving/internal/domain/recommend"
	"github.com/claudioed/task-interleaving/internal/domain/travel"
)

// StationTrip is the aggregate root: one per station. It holds the station's
// position (the end of the last task it completed), that task and its instant,
// and the most recent hint.
//
// Invariants: the position only moves forward in time (a completion not strictly
// newer than the recorded one is ignored); at most one hint is Active (issuing a
// hint supersedes the Active one); a hint holds 1..MaxCandidates distinct,
// eligible candidates within MaxDetourM, ranked 1..n by detour then task id; a
// hint expires at issued_at + HintTTL; a claim of one of its candidates by the
// hinted station while it is unexpired marks it Consumed, any other claim leaves
// it Active. Version is 0 for a new trip and +1 per accepted transition.
type StationTrip struct {
	id       StationID
	position Position
	lastTask recommend.TaskID
	lastAt   time.Time
	hint     *Hint
	version  int64
	events   []Event
}

// New creates the trip of a station that has recorded nothing yet.
func New(id StationID) (*StationTrip, error) {
	if _, err := NewStationID(string(id)); err != nil {
		return nil, err
	}
	return &StationTrip{id: id}, nil
}

// ID returns the station id.
func (s *StationTrip) ID() StationID { return s.id }

// Version returns the aggregate version.
func (s *StationTrip) Version() int64 { return s.version }

// Position returns where the station stands; false until a completion was
// recorded. The returned point is a copy.
func (s *StationTrip) Position() (Position, bool) {
	if s.lastAt.IsZero() {
		return Position{}, false
	}
	return Position{Site: s.position.Site, Place: copyPlace(s.position.Place)}, true
}

// LastCompleted returns the last completed task and when it completed (zero
// values until a completion was recorded).
func (s *StationTrip) LastCompleted() (recommend.TaskID, time.Time) {
	return s.lastTask, s.lastAt
}

// Hint returns the most recent hint, in any state; false if none was issued.
func (s *StationTrip) Hint() (Hint, bool) {
	if s.hint == nil {
		return Hint{}, false
	}
	return s.hint.clone(), true
}

// ActiveHint returns the Active hint; false if there is none.
func (s *StationTrip) ActiveHint() (Hint, bool) {
	if s.hint == nil || s.hint.State != StateActive {
		return Hint{}, false
	}
	return s.hint.clone(), true
}

// RecordCompletion records that the station completed task at the given instant
// and now stands at pos. A completion not strictly newer than the recorded one is
// ignored: it returns false and changes nothing.
func (s *StationTrip) RecordCompletion(task recommend.TaskID, at time.Time, pos Position) (bool, error) {
	if err := validateCompletion(task, at, pos); err != nil {
		return false, err
	}
	if !s.lastAt.IsZero() && !at.After(s.lastAt) {
		return false, nil
	}
	s.position = Position{Site: pos.Site, Place: copyPlace(pos.Place)}
	s.lastTask = task
	s.lastAt = at.UTC()
	s.version++
	return true, nil
}

// IssueHint issues a hint with the given candidates under the site's policy.
// Any Active hint becomes Superseded. The candidates must satisfy the hint
// invariants (see the type doc); an empty set is refused: it issues no hint. The
// hint expires at now + the policy's HintTTL. It raises HintIssued.
func (s *StationTrip) IssueHint(id HintID, candidates []recommend.Candidate, p policy.Policy, now time.Time) error {
	if s.lastAt.IsZero() {
		return ErrNoPosition
	}
	if _, err := NewHintID(string(id)); err != nil {
		return err
	}
	if now.IsZero() {
		return ErrInvalidTime
	}
	if p.SiteID() != s.position.Site {
		return ErrSiteMismatch
	}
	if err := validateCandidates(candidates, p); err != nil {
		return err
	}
	var superseded HintID
	if s.hint != nil && s.hint.State == StateActive {
		s.hint.State = StateSuperseded
		superseded = s.hint.ID
	}
	h := Hint{
		ID:            id,
		AfterTask:     s.lastTask,
		Origin:        s.position.Place.Location,
		IssuedAt:      now.UTC(),
		ExpiresAt:     now.UTC().Add(p.HintTTL()),
		PolicyVersion: p.Version(),
		Candidates:    append([]recommend.Candidate(nil), candidates...),
		State:         StateActive,
	}
	s.hint = &h
	s.version++
	s.raise(HintIssued{
		Header:           s.header(now),
		HintID:           h.ID,
		AfterTask:        h.AfterTask,
		Origin:           h.Origin,
		ExpiresAt:        h.ExpiresAt,
		PolicyVersion:    h.PolicyVersion,
		Candidates:       append([]recommend.Candidate(nil), h.Candidates...),
		SupersededHintID: superseded,
	})
	return nil
}

// RecordClaim records that the station claimed task at the given instant. It
// marks the Active hint Consumed only when the task is one of its candidates, the
// claim is not before the hint was issued and the hint is unexpired; otherwise it
// changes nothing. It returns whether the hint was consumed.
func (s *StationTrip) RecordClaim(task recommend.TaskID, now time.Time) bool {
	if s.hint == nil || s.hint.State != StateActive {
		return false
	}
	if now.Before(s.hint.IssuedAt) || !now.Before(s.hint.ExpiresAt) {
		return false
	}
	c, ok := findCandidate(s.hint.Candidates, task)
	if !ok {
		return false
	}
	s.hint.State = StateConsumed
	s.hint.ConsumedTask = task
	s.hint.ConsumedAt = now.UTC()
	s.version++
	s.raise(HintConsumed{
		Header:  s.header(now),
		HintID:  s.hint.ID,
		TaskID:  task,
		Rank:    c.Rank,
		DetourM: c.DetourM,
	})
	return true
}

// Expire marks the Active hint Expired when now is at or after its expiry. It
// returns whether the hint expired.
func (s *StationTrip) Expire(now time.Time) bool {
	if s.hint == nil || s.hint.State != StateActive {
		return false
	}
	if now.Before(s.hint.ExpiresAt) {
		return false
	}
	s.hint.State = StateExpired
	s.version++
	s.raise(HintExpired{Header: s.header(now), HintID: s.hint.ID, ExpiresAt: s.hint.ExpiresAt})
	return true
}

// DomainEvents returns the facts raised since the last ClearDomainEvents, in
// order. The slice is a copy.
func (s *StationTrip) DomainEvents() []Event {
	return append([]Event(nil), s.events...)
}

// ClearDomainEvents forgets the raised facts once they have been handed to the
// outbox.
func (s *StationTrip) ClearDomainEvents() {
	s.events = nil
}

func (s *StationTrip) raise(e Event) {
	s.events = append(s.events, e)
}

func (s *StationTrip) header(at time.Time) Header {
	return Header{Station: s.id, Site: s.position.Site, Version: s.version, At: at.UTC()}
}

func validateCompletion(task recommend.TaskID, at time.Time, pos Position) error {
	if strings.TrimSpace(string(task)) == "" {
		return ErrInvalidTask
	}
	if at.IsZero() {
		return ErrInvalidTime
	}
	if _, err := policy.NewSiteID(string(pos.Site)); err != nil {
		return ErrInvalidPosition
	}
	if strings.TrimSpace(pos.Place.Location) == "" {
		return ErrInvalidPosition
	}
	return nil
}

func validateCandidates(cs []recommend.Candidate, p policy.Policy) error {
	if len(cs) == 0 {
		return ErrNoCandidates
	}
	if len(cs) > p.MaxCandidates() {
		return ErrTooManyCandidates
	}
	seen := make(map[recommend.TaskID]struct{}, len(cs))
	for i, c := range cs {
		if err := validateCandidate(c, i, p); err != nil {
			return err
		}
		if _, dup := seen[c.TaskID]; dup {
			return ErrDuplicateCandidate
		}
		seen[c.TaskID] = struct{}{}
		if i > 0 && rankedBefore(c, cs[i-1]) {
			return ErrCandidateRanking
		}
	}
	return nil
}

func validateCandidate(c recommend.Candidate, index int, p policy.Policy) error {
	if strings.TrimSpace(string(c.TaskID)) == "" {
		return ErrInvalidTask
	}
	if c.Rank != index+1 {
		return ErrCandidateRanking
	}
	if !p.Eligible(c.TaskType) {
		return ErrCandidateNotEligible
	}
	if math.IsNaN(c.DetourM) || c.DetourM < 0 || c.DetourM > p.MaxDetourM() {
		return ErrCandidateDetour
	}
	return nil
}

// rankedBefore reports whether a sorts strictly before b by detour, then task id.
func rankedBefore(a, b recommend.Candidate) bool {
	// cmp.Compare yields exactly -1, 0 or +1, so "== -1" is "a sorts first".
	return cmp.Or(cmp.Compare(a.DetourM, b.DetourM), cmp.Compare(a.TaskID, b.TaskID)) == -1
}

func findCandidate(cs []recommend.Candidate, task recommend.TaskID) (recommend.Candidate, bool) {
	for _, c := range cs {
		if c.TaskID == task {
			return c, true
		}
	}
	return recommend.Candidate{}, false
}

func copyPlace(p travel.Place) travel.Place {
	if p.Point == nil {
		return p
	}
	pt := *p.Point
	return travel.Place{Location: p.Location, Point: &pt}
}
