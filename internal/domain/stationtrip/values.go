// Package stationtrip holds the StationTrip aggregate: one per station, it
// records where the operator stands (the end of the last completed task) and the
// interleaving hint issued there. A hint is ADVISORY: it lists a few nearby open
// tasks, expires on a TTL and is never an assignment. The candidates are computed
// elsewhere (package recommend); this package enforces what a valid hint is and
// which transitions a hint may go through.
package stationtrip

import (
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/claudioed/task-interleaving/internal/domain/policy"
	"github.com/claudioed/task-interleaving/internal/domain/recommend"
	"github.com/claudioed/task-interleaving/internal/domain/travel"
)

// Validation and rule errors. The application layer maps them to problem slugs.
var (
	ErrInvalidStationID     = errors.New("stationtrip: invalid station id")
	ErrInvalidHintID        = errors.New("stationtrip: invalid hint id")
	ErrInvalidTask          = errors.New("stationtrip: invalid task id")
	ErrInvalidTime          = errors.New("stationtrip: the instant must not be zero")
	ErrInvalidPosition      = errors.New("stationtrip: invalid position")
	ErrNoPosition           = errors.New("stationtrip: the station has no recorded position")
	ErrSiteMismatch         = errors.New("stationtrip: the policy belongs to another site than the station")
	ErrNoCandidates         = errors.New("stationtrip: an empty candidate set issues no hint")
	ErrTooManyCandidates    = errors.New("stationtrip: more candidates than the policy allows")
	ErrDuplicateCandidate   = errors.New("stationtrip: a task appears twice among the candidates")
	ErrCandidateNotEligible = errors.New("stationtrip: candidate task type is not eligible under the policy")
	ErrCandidateDetour      = errors.New("stationtrip: candidate detour is negative or exceeds the policy")
	ErrCandidateRanking     = errors.New("stationtrip: candidates are not ranked 1..n by detour then task id")
)

const (
	maxTokenLen  = 64
	hintIDPrefix = "hint-"
)

// StationID identifies a station (the id fulfillment-execution claims under).
type StationID string

// HintID identifies an interleaving hint: "hint-" followed by a unique suffix.
type HintID string

// validToken reports whether s is 1..64 characters with no whitespace, control
// characters or '/'.
func validToken(s string) bool {
	if s == "" || utf8.RuneCountInString(s) > maxTokenLen {
		return false
	}
	for _, r := range s {
		if r == '/' || unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// NewStationID validates a station id.
func NewStationID(s string) (StationID, error) {
	if !validToken(s) {
		return "", ErrInvalidStationID
	}
	return StationID(s), nil
}

// NewHintID validates a hint id: the "hint-" prefix, a non-empty suffix and the
// token rules.
func NewHintID(s string) (HintID, error) {
	if !validToken(s) || !strings.HasPrefix(s, hintIDPrefix) || len(s) == len(hintIDPrefix) {
		return "", ErrInvalidHintID
	}
	return HintID(s), nil
}

// HintState is the lifecycle state of a hint.
type HintState string

// Hint states. Active moves to exactly one terminal state.
const (
	StateActive     HintState = "Active"
	StateConsumed   HintState = "Consumed"
	StateExpired    HintState = "Expired"
	StateSuperseded HintState = "Superseded"
)

// Valid reports whether s is a known state.
func (s HintState) Valid() bool {
	if s == StateActive || s == StateConsumed {
		return true
	}
	return s == StateExpired || s == StateSuperseded
}

// Terminal reports whether no transition leaves the state.
func (s HintState) Terminal() bool {
	return s.Valid() && s != StateActive
}

// Position is where a station stands: its site and the end location of the last
// task it completed, with geometry when known.
type Position struct {
	Site  policy.SiteID
	Place travel.Place
}

// Hint is the interleaving hint issued to a station.
type Hint struct {
	ID            HintID
	AfterTask     recommend.TaskID
	Origin        string
	IssuedAt      time.Time
	ExpiresAt     time.Time
	PolicyVersion int64
	Candidates    []recommend.Candidate
	State         HintState
	// ConsumedTask and ConsumedAt are set only in state Consumed.
	ConsumedTask recommend.TaskID
	ConsumedAt   time.Time
}

func (h Hint) clone() Hint {
	h.Candidates = append([]recommend.Candidate(nil), h.Candidates...)
	return h
}
