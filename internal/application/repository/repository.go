// Package repository holds the non-interface vocabulary of the application's
// ports: typed errors, list filters, the application-level event record and the
// value types of the event-fed local copies (ADR 0003). It is separate from
// package ports because ports may contain interfaces only (internal/architecture's
// "ports package only contains interfaces" rule).
package repository

import (
	"errors"
	"time"

	"github.com/claudioed/task-interleaving/internal/domain/policy"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
	"github.com/claudioed/task-interleaving/internal/domain/travel"
)

var (
	// ErrPolicyNotFound is returned when a site has declared no policy.
	ErrPolicyNotFound = errors.New("interleaving policy not found")
	// ErrStationNotFound is returned when no completion was recorded for a station.
	ErrStationNotFound = errors.New("station not found")
	// ErrHintNotFound is returned when a hint id is unknown.
	ErrHintNotFound = errors.New("interleaving hint not found")
	// ErrConcurrentModification is returned by a version-guarded Save when the
	// stored version is not the version the caller loaded: another writer won
	// the race.
	ErrConcurrentModification = errors.New("concurrent modification")
)

// PolicyRecord is a stored policy with the time it was last declared.
type PolicyRecord struct {
	Policy     policy.Policy
	DeclaredAt time.Time
}

// PolicyDeclaration is the application-level record of the analytics-only
// InterleavingPolicyDeclared fact. The policy domain package raises no event
// for it, so the use case builds this record and the event adapter maps it to
// the CloudEvent (ADR 0004).
type PolicyDeclaration struct {
	SiteID            string
	MaxDetourM        float64
	MaxCandidates     int
	HintTTLSeconds    int64
	EligibleTaskTypes []string
	VerticalWeight    float64
	Version           int64
	DeclaredAt        time.Time
}

// NewPolicyDeclaration builds the declaration fact of p declared at the given
// instant.
func NewPolicyDeclaration(p policy.Policy, at time.Time) PolicyDeclaration {
	types := make([]string, 0, len(p.EligibleTaskTypes()))
	for _, t := range p.EligibleTaskTypes() {
		types = append(types, string(t))
	}
	return PolicyDeclaration{
		SiteID:            string(p.SiteID()),
		MaxDetourM:        p.MaxDetourM(),
		MaxCandidates:     p.MaxCandidates(),
		HintTTLSeconds:    int64(p.HintTTL() / time.Second),
		EligibleTaskTypes: types,
		VerticalWeight:    p.VerticalWeight(),
		Version:           p.Version(),
		DeclaredAt:        at.UTC(),
	}
}

// HintFilter narrows StationTripRepo.ListHints. Empty values mean "no filter".
type HintFilter struct {
	StationID stationtrip.StationID
	SiteID    policy.SiteID
	State     stationtrip.HintState
}

// HintRecord is a hint of the history, with the station and site it was issued
// to. Version starts at 1 and grows by one per state change. SupersededAt is the
// instant of the newer hint that replaced it; zero unless Superseded.
type HintRecord struct {
	StationID    stationtrip.StationID
	SiteID       policy.SiteID
	Hint         stationtrip.Hint
	Version      int64
	SupersededAt time.Time
}

// TaskState is the state of a task in the local task pool copy.
type TaskState string

// Task states of the pool.
const (
	TaskAvailable TaskState = "AVAILABLE"
	TaskClaimed   TaskState = "CLAIMED"
	TaskCompleted TaskState = "COMPLETED"
)

// Task is one row of the task pool copy (ADR 0003). A task learned only from a
// claim or a completion is minimal: its site and locations stay empty.
type Task struct {
	ID                   string
	Type                 string
	SiteID               string
	FromLocation         string
	ToLocation           string
	RequiredCapabilities []string
	PriorityBand         string
	State                TaskState
	ClaimedBy            string
}

// Zone is a facility-layout zone of the layout copy.
type Zone struct {
	ID       string
	SiteCode string
	ZoneCode string
}

// Place is a location of the layout copy joined with its zone's site and its
// geometry. SiteCode is empty while the zone has not been seen; Point is nil
// while the geometry has not been seen.
type Place struct {
	Code           string
	SiteCode       string
	Point          *travel.Point
	Decommissioned bool
}

// IdempotencyRecord is the stored answer of a POST made with an Idempotency-Key:
// 2xx and 4xx only, never 5xx. Fingerprint identifies the request body so the
// same key with a different body can be told apart.
type IdempotencyRecord struct {
	Key         string
	Fingerprint string
	Status      int
	ContentType string
	Body        []byte
	CreatedAt   time.Time
}
