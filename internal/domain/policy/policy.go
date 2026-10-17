// Package policy holds the InterleavingPolicy aggregate: the per-site settings
// that decide whether and how interleaving hints are issued (how far a task may
// be, how many are listed, how long a hint lives, which task types qualify, how
// vertical travel is weighed). A site that never declared a policy is inert.
package policy

import (
	"errors"
	"math"
	"slices"
	"time"
	"unicode"
	"unicode/utf8"
)

// Validation errors. The application layer maps them to problem slugs.
var (
	ErrInvalidSiteID     = errors.New("policy: invalid site id")
	ErrInvalidMaxDetour  = errors.New("policy: max detour must be a finite number greater than 0")
	ErrInvalidCandidates = errors.New("policy: max candidates must be between 1 and 5")
	ErrInvalidHintTTL    = errors.New("policy: hint ttl must be greater than 0")
	ErrInvalidTaskTypes  = errors.New("policy: eligible task types must be a non-empty set of known task types")
	ErrInvalidWeight     = errors.New("policy: vertical weight must be a finite number of at least 0")
	ErrInvalidVersion    = errors.New("policy: version must be at least 1")
)

const (
	maxTokenLen   = 64
	minCandidates = 1
	maxCandidates = 5
)

// SiteID identifies a site (the fleet's site code).
type SiteID string

// NewSiteID validates a site id: 1..64 characters with no whitespace, control
// characters or '/'.
func NewSiteID(s string) (SiteID, error) {
	if s == "" || utf8.RuneCountInString(s) > maxTokenLen {
		return "", ErrInvalidSiteID
	}
	for _, r := range s {
		if r == '/' || unicode.IsSpace(r) || unicode.IsControl(r) {
			return "", ErrInvalidSiteID
		}
	}
	return SiteID(s), nil
}

// TaskType is the type of a floor task, as fulfillment-execution names it.
type TaskType string

// Task types a hint may point at.
const (
	TaskPick      TaskType = "PICK"
	TaskPack      TaskType = "PACK"
	TaskRebin     TaskType = "REBIN"
	TaskSlam      TaskType = "SLAM"
	TaskRelocate  TaskType = "RELOCATE"
	TaskReplenish TaskType = "REPLENISH"
)

// Valid reports whether t is a known task type.
func (t TaskType) Valid() bool {
	if t == TaskPick || t == TaskPack {
		return true
	}
	if t == TaskRebin || t == TaskSlam {
		return true
	}
	return t == TaskRelocate || t == TaskReplenish
}

// Settings are the values an operator declares for a site.
type Settings struct {
	MaxDetourM        float64
	MaxCandidates     int
	HintTTL           time.Duration
	EligibleTaskTypes []TaskType
	VerticalWeight    float64
}

// Policy is the aggregate root: the settings of one site at one version.
// It is immutable; WithChanges returns the next version.
//
// Invariants: MaxDetourM > 0 and finite; 1 <= MaxCandidates <= 5; HintTTL > 0;
// EligibleTaskTypes is a non-empty set of known task types; VerticalWeight >= 0
// and finite; Version >= 1 and +1 per change.
type Policy struct {
	site     SiteID
	settings Settings
	version  int64
}

// New declares the first version (1) of a site's policy.
func New(site SiteID, s Settings) (Policy, error) {
	return build(site, s, 1)
}

// Restore rebuilds a persisted policy, validating every invariant, so a corrupt
// row can never become a live aggregate.
func Restore(site SiteID, s Settings, version int64) (Policy, error) {
	if version < 1 {
		return Policy{}, ErrInvalidVersion
	}
	return build(site, s, version)
}

// WithChanges returns the next version of the policy with the given settings,
// which replace the previous ones as a whole.
func (p Policy) WithChanges(s Settings) (Policy, error) {
	return build(p.site, s, p.version+1)
}

func build(site SiteID, s Settings, version int64) (Policy, error) {
	if _, err := NewSiteID(string(site)); err != nil {
		return Policy{}, err
	}
	types, err := validate(s)
	if err != nil {
		return Policy{}, err
	}
	s.EligibleTaskTypes = types
	return Policy{site: site, settings: s, version: version}, nil
}

func validate(s Settings) ([]TaskType, error) {
	if !finite(s.MaxDetourM) || s.MaxDetourM <= 0 {
		return nil, ErrInvalidMaxDetour
	}
	if s.MaxCandidates < minCandidates || s.MaxCandidates > maxCandidates {
		return nil, ErrInvalidCandidates
	}
	if s.HintTTL <= 0 {
		return nil, ErrInvalidHintTTL
	}
	if !finite(s.VerticalWeight) || s.VerticalWeight < 0 {
		return nil, ErrInvalidWeight
	}
	return normaliseTypes(s.EligibleTaskTypes)
}

// normaliseTypes returns the sorted, de-duplicated set of types, or an error
// when it is empty or holds an unknown type.
func normaliseTypes(in []TaskType) ([]TaskType, error) {
	if len(in) == 0 {
		return nil, ErrInvalidTaskTypes
	}
	out := make([]TaskType, 0, len(in))
	for _, t := range in {
		if !t.Valid() {
			return nil, ErrInvalidTaskTypes
		}
		out = append(out, t)
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

func finite(f float64) bool {
	if math.IsNaN(f) {
		return false
	}
	return !math.IsInf(f, 0)
}

// SiteID returns the site the policy is for.
func (p Policy) SiteID() SiteID { return p.site }

// MaxDetourM returns the largest detour, in metres, a candidate may have.
func (p Policy) MaxDetourM() float64 { return p.settings.MaxDetourM }

// MaxCandidates returns how many candidates a hint lists at most.
func (p Policy) MaxCandidates() int { return p.settings.MaxCandidates }

// HintTTL returns how long a hint stays valid.
func (p Policy) HintTTL() time.Duration { return p.settings.HintTTL }

// VerticalWeight returns the weight of vertical travel.
func (p Policy) VerticalWeight() float64 { return p.settings.VerticalWeight }

// Version returns the aggregate version (1 at declaration, +1 per change).
func (p Policy) Version() int64 { return p.version }

// EligibleTaskTypes returns a copy of the sorted set of eligible task types.
func (p Policy) EligibleTaskTypes() []TaskType {
	return slices.Clone(p.settings.EligibleTaskTypes)
}

// Eligible reports whether a hint may point at a task of type t.
func (p Policy) Eligible(t TaskType) bool {
	return slices.Contains(p.settings.EligibleTaskTypes, t)
}

// Settings returns a copy of the settings.
func (p Policy) Settings() Settings {
	s := p.settings
	s.EligibleTaskTypes = slices.Clone(s.EligibleTaskTypes)
	return s
}
