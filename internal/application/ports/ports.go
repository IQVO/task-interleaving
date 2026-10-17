// Package ports declares the application's OUT ports: interfaces only
// (enforced by internal/architecture). Their non-interface vocabulary
// (errors, filters, value types) lives in internal/application/repository.
package ports

import (
	"context"
	"time"

	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/domain/policy"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
)

// PolicyRepo persists the interleaving policy of each site. Inside a
// UnitOfWork it joins the transaction carried in ctx.
type PolicyRepo interface {
	// Get returns the policy, or repository.ErrPolicyNotFound.
	Get(ctx context.Context, site policy.SiteID) (repository.PolicyRecord, error)
	// Save persists rec guarded by the version the caller loaded: loadedVersion
	// 0 inserts (an existing policy is repository.ErrConcurrentModification);
	// any other value updates only when the stored version still equals
	// loadedVersion (else repository.ErrConcurrentModification).
	Save(ctx context.Context, rec repository.PolicyRecord, loadedVersion int64) error
}

// StationTripRepo persists StationTrip aggregates by station id and keeps the
// history of their hints. Inside a UnitOfWork it joins the transaction carried
// in ctx.
type StationTripRepo interface {
	// Get returns the trip, or repository.ErrStationNotFound.
	Get(ctx context.Context, id stationtrip.StationID) (*stationtrip.StationTrip, error)
	// Save persists trip guarded by the version the caller loaded: loadedVersion
	// 0 inserts (an existing station is repository.ErrConcurrentModification);
	// any other value updates only when the stored version still equals
	// loadedVersion (else repository.ErrConcurrentModification). It also records
	// the trip's current hint in the hint history, and closes the station's
	// previous Active hint as Superseded when the current hint is a newer one.
	Save(ctx context.Context, trip *stationtrip.StationTrip, loadedVersion int64) error
	// ExpiredActive returns, ordered by station id and limited to limit, the
	// stations whose Active hint has an expiry at or before now.
	ExpiredActive(ctx context.Context, now time.Time, limit int) ([]stationtrip.StationID, error)
	// GetHint returns a hint of the history, or repository.ErrHintNotFound.
	GetHint(ctx context.Context, id stationtrip.HintID) (repository.HintRecord, error)
	// ListHints returns up to limit hints newest first (issued-at, then id,
	// descending) that come after the hint `after` in that order ("" = from the
	// start), narrowed by filter.
	ListHints(ctx context.Context, filter repository.HintFilter, after stationtrip.HintID, limit int) ([]repository.HintRecord, error)
}

// TaskPool is the event-fed local copy of the open-task pool and of who claimed
// what (ADR 0003). Inside a UnitOfWork it joins the transaction carried in ctx.
type TaskPool interface {
	// Get returns the task; false when it was never seen.
	Get(ctx context.Context, id string) (repository.Task, bool, error)
	// Save upserts the task.
	Save(ctx context.Context, t repository.Task) error
	// Available returns the AVAILABLE tasks of the site ordered by task id.
	Available(ctx context.Context, siteID string) ([]repository.Task, error)
}

// LocationBook is the event-fed local copy of the layout (zones and locations)
// and of the location geometry (ADR 0003). Inside a UnitOfWork it joins the
// transaction carried in ctx.
type LocationBook interface {
	// Places returns, for each code that was ever seen (registered or with
	// geometry), the location joined with its zone's site and its geometry.
	// Unseen codes are absent from the result.
	Places(ctx context.Context, codes []string) (map[string]repository.Place, error)
	// SaveZone upserts a zone.
	SaveZone(ctx context.Context, z repository.Zone) error
	// SaveLocation upserts a location of a zone; a decommissioned location
	// stays decommissioned.
	SaveLocation(ctx context.Context, code, zoneID string) error
	// DecommissionLocation retires a location for good (also when it was never
	// seen).
	DecommissionLocation(ctx context.Context, code string) error
	// SaveGeometry upserts the point of a location, whether or not the
	// location was registered yet.
	SaveGeometry(ctx context.Context, code string, x, y, z float64) error
}

// EventSink is the write side of the transactional outbox, in domain terms.
// Called with the ctx handed to UnitOfWork.Do it joins the SAME transaction as
// the aggregate's Save, so the state change and its events commit or roll back
// together. The outbox adapter maps the facts to CloudEvents (ADR 0004).
type EventSink interface {
	// PublishTripEvents enqueues the facts a StationTrip raised.
	PublishTripEvents(ctx context.Context, events ...stationtrip.Event) error
	// PublishPolicyDeclared enqueues the analytics-only InterleavingPolicyDeclared fact.
	PublishPolicyDeclared(ctx context.Context, d repository.PolicyDeclaration) error
}

// ProcessedEvents is the inbound-consumer idempotency guard. Claim records
// (consumer, CloudEvents id) INSIDE the same UnitOfWork as the event's effect
// and reports false when an earlier committed handling already claimed it
// (the caller then skips).
type ProcessedEvents interface {
	Claim(ctx context.Context, consumer, eventID string) (claimed bool, err error)
}

// IdempotencyStore keeps the answers of POSTs made with an Idempotency-Key.
// The HTTP adapter stores 2xx and 4xx answers only, never 5xx.
type IdempotencyStore interface {
	// Lookup returns the stored answer for the key.
	Lookup(ctx context.Context, key string) (repository.IdempotencyRecord, bool, error)
	// Save stores the answer; the first writer of a key wins and a later Save
	// of the same key changes nothing.
	Save(ctx context.Context, rec repository.IdempotencyRecord) error
}
