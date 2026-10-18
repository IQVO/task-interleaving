// Package report is the self-contained read-model region of the analytics read
// side (ADR 0008): the shape of the hint-adoption report, the query rules (date
// range, site scope, day windows, granularity), the pure percentile, rate and
// freshness logic, the validation of a projectable event, and the writer/reader
// ports the analytical store implements. It imports no other internal package,
// so the OLTP domain can never leak into the projection and the arch test keeps
// this region isolated (mirroring slotting-optimization ADR 0006).
package report

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Kind is which of the four published analytics events a fact came from. The
// names are the CloudEvents event names (the last segment of `type`).
type Kind string

// The four events the analytics topic carries.
const (
	KindHintIssued     Kind = "InterleavingHintIssued"
	KindHintConsumed   Kind = "InterleavingHintConsumed"
	KindHintExpired    Kind = "InterleavingHintExpired"
	KindPolicyDeclared Kind = "InterleavingPolicyDeclared"
)

// Kinds returns every projected kind, in catalogue order.
func Kinds() []Kind {
	return []Kind{KindHintIssued, KindHintConsumed, KindHintExpired, KindPolicyDeclared}
}

// Known reports whether k is one of the four published kinds.
func (k Kind) Known() bool {
	for _, c := range Kinds() {
		if k == c {
			return true
		}
	}
	return false
}

// IsHint reports whether k is one of the three hint events (the ones that fold
// into hint_facts).
func (k Kind) IsHint() bool { return k.Known() && k != KindPolicyDeclared }

// Event is one decoded analytics event ready to project. The three hint kinds
// carry the hint id, the station and the site; the policy kind carries only
// the site.
type Event struct {
	Kind    Kind
	EventID string    // CloudEvents id: the idempotency key
	At      time.Time // CloudEvents time: when the event occurred
	HintID  string
	Station string
	SiteID  string

	// ExpiresAt and CandidateCount are set on InterleavingHintIssued.
	ExpiresAt      time.Time
	CandidateCount int
	// Rank (1-based) and DetourM are set on InterleavingHintConsumed.
	Rank    int
	DetourM float64
}

// ErrInvalidEvent marks an event that can never be projected (the consumer
// dead-letters it).
var ErrInvalidEvent = errors.New("report: event cannot be projected")

// Validate checks the invariants every store relies on. Every failure wraps
// ErrInvalidEvent and is deterministic.
func (e Event) Validate() error {
	switch {
	case !e.Kind.Known():
		return fmt.Errorf("%w: unknown kind %q", ErrInvalidEvent, e.Kind)
	case e.EventID == "":
		return fmt.Errorf("%w: %s without an id", ErrInvalidEvent, e.Kind)
	case e.At.IsZero():
		return fmt.Errorf("%w: %s %s without a time", ErrInvalidEvent, e.Kind, e.EventID)
	case e.SiteID == "":
		return fmt.Errorf("%w: %s %s without a site_id", ErrInvalidEvent, e.Kind, e.EventID)
	case !e.Kind.IsHint():
		return nil
	case e.HintID == "":
		return fmt.Errorf("%w: %s %s without a hint_id", ErrInvalidEvent, e.Kind, e.EventID)
	case e.Station == "":
		return fmt.Errorf("%w: %s %s without a station_id", ErrInvalidEvent, e.Kind, e.EventID)
	case e.Kind == KindHintIssued && e.CandidateCount < 1:
		return fmt.Errorf("%w: %s %s without candidates", ErrInvalidEvent, e.Kind, e.EventID)
	case e.Kind == KindHintConsumed && e.Rank < 1:
		return fmt.Errorf("%w: %s %s with a rank below 1", ErrInvalidEvent, e.Kind, e.EventID)
	case e.Kind == KindHintConsumed && e.DetourM < 0:
		return fmt.Errorf("%w: %s %s with a negative detour_m", ErrInvalidEvent, e.Kind, e.EventID)
	}
	return nil
}

// Milestones is the "this happened at" candidate an event offers for each
// milestone column of hint_facts (nil where it offers none). A store keeps the
// earliest candidate per milestone, so arrival order never matters.
type Milestones struct {
	Issued   *time.Time
	Consumed *time.Time
	Expired  *time.Time
}

// MilestonesOf returns the milestone an event marks on its hint row (none for a
// policy declaration).
func MilestonesOf(e Event) Milestones {
	at := e.At.UTC()
	var m Milestones
	switch e.Kind {
	case KindHintIssued:
		m.Issued = &at
	case KindHintConsumed:
		m.Consumed = &at
	case KindHintExpired:
		m.Expired = &at
	case KindPolicyDeclared:
		// no hint row
	}
	return m
}

// Projection is the WRITER port: Apply records the event id and folds the
// event into the model in ONE transaction. applied is false when the id was
// already recorded (a replay): nothing changed. An error wrapping ErrRejected
// is deterministic (the store can never accept this event); any other error is
// transient and the same event may be retried.
type Projection interface {
	Apply(ctx context.Context, e Event) (applied bool, err error)
}

// ErrRejected marks an event the analytical store deterministically refuses (a
// data-exception or integrity-violation class error).
var ErrRejected = errors.New("report: event rejected by the analytical store")

// Reader is the READER port.
type Reader interface {
	// AdoptionDays returns one row per DayWindow of the query, in day order,
	// zeros included.
	AdoptionDays(ctx context.Context, q AdoptionQuery) ([]AdoptionDay, error)
	// LastEventAt is the CloudEvents time of the newest event the projection
	// has applied (nil while it has applied none): the freshness basis.
	LastEventAt(ctx context.Context) (*time.Time, error)
}

// AdoptionQuery selects the adoption rows: the time range, the optional site
// scope ("" = every site) and the bucket granularity (day only today).
type AdoptionQuery struct {
	Range       Range
	SiteID      string
	Granularity Granularity
}

// AdoptionDay is one UTC day of the hint-adoption report, counted over the
// day's window (the part of the day inside the requested range).
type AdoptionDay struct {
	// Day is the UTC calendar day, at midnight UTC.
	Day time.Time
	// Issued counts the hints whose issued_at fell in the window.
	Issued int
	// Consumed counts the hints whose consumed_at fell in the window: the
	// operator took one of the candidates.
	Consumed int
	// Expired counts the hints whose expired_at fell in the window: nobody took
	// a candidate before the TTL ran out.
	Expired int
	// ConsumedFirstRank counts, among Consumed, the hints whose consumed
	// candidate was rank 1 (the nearest one).
	ConsumedFirstRank int
	// MeanDetourM is the mean detour of the consumed candidates, nil while
	// Consumed is 0.
	MeanDetourM *float64
	// TimeToConsume is the issued-to-consumed time of the hints consumed in the
	// window whose issue the projection also saw.
	TimeToConsume Timing
}

// AdoptionRate is consumed / (consumed + expired): of the hints that ended in
// the window with an event, the share an operator acted on. A hint superseded by
// a newer one emits neither event and is not in the denominator. nil while no
// hint ended in the window.
func AdoptionRate(consumed, expired int) *float64 {
	ended := consumed + expired
	if ended == 0 {
		return nil
	}
	rate := float64(consumed) / float64(ended)
	return &rate
}
