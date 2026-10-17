package stationtrip

import (
	"time"

	"github.com/claudioed/task-interleaving/internal/domain/policy"
	"github.com/claudioed/task-interleaving/internal/domain/recommend"
)

// Event is a domain fact raised by the StationTrip aggregate. It carries the
// station, the site, the aggregate version AFTER the change and when it
// happened (from the caller's clock). It is not a wire message: the adapter
// that publishes it builds the CloudEvent.
type Event interface {
	EventName() string
	StationID() StationID
	SiteID() policy.SiteID
	StationVersion() int64
	OccurredAt() time.Time
}

// Header is embedded in every event.
type Header struct {
	Station StationID
	Site    policy.SiteID
	Version int64
	At      time.Time
}

// StationID returns the station the event is about.
func (h Header) StationID() StationID { return h.Station }

// SiteID returns the site of the station.
func (h Header) SiteID() policy.SiteID { return h.Site }

// StationVersion returns the aggregate version after the change.
func (h Header) StationVersion() int64 { return h.Version }

// OccurredAt returns when the change happened.
func (h Header) OccurredAt() time.Time { return h.At }

// HintIssued is raised when a hint is issued. SupersededHintID names the Active
// hint it replaced, if any, so persistence can close that hint.
type HintIssued struct {
	Header
	HintID           HintID
	AfterTask        recommend.TaskID
	Origin           string
	ExpiresAt        time.Time
	PolicyVersion    int64
	Candidates       []recommend.Candidate
	SupersededHintID HintID
}

// EventName returns "HintIssued".
func (HintIssued) EventName() string { return "HintIssued" }

// HintConsumed is raised when the hinted station claims one of the candidates.
// Rank and DetourM are those of the claimed candidate; At is the claim time.
type HintConsumed struct {
	Header
	HintID  HintID
	TaskID  recommend.TaskID
	Rank    int
	DetourM float64
}

// EventName returns "HintConsumed".
func (HintConsumed) EventName() string { return "HintConsumed" }

// HintExpired is raised when an Active hint reaches its expiry; At is the
// instant the expiry was recorded, at or after ExpiresAt.
type HintExpired struct {
	Header
	HintID    HintID
	ExpiresAt time.Time
}

// EventName returns "HintExpired".
func (HintExpired) EventName() string { return "HintExpired" }
