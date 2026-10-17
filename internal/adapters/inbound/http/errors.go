package http

import (
	"errors"
	"net/http"
	"strings"

	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/application/usecases"
	"github.com/claudioed/task-interleaving/internal/domain/policy"
	"github.com/claudioed/task-interleaving/internal/domain/recommend"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
)

// problemBaseURI is the namespace of this service's RFC 7807 `type` URIs.
const problemBaseURI = "https://errors.task-interleaving.warehouse-systems.dev/"

// Errors the adapter itself raises, before or besides the use cases.
var (
	// errMalformedRequest marks a body the adapter rejects (bad JSON, unknown
	// field, wrong type, trailing data, empty required body, a value outside
	// the request schema).
	errMalformedRequest = errors.New("request body is not valid JSON for this operation")
	// errHintTTLTooLarge marks a hintTtlSeconds beyond the contract's int32.
	errHintTTLTooLarge = errors.New("hintTtlSeconds is too large")
	// errInvalidLocation marks a fromLocation outside the contract's bounds.
	errInvalidLocation = errors.New("fromLocation must be 1 to 128 characters and not blank")
)

// problem is the fixed (status, slug, title, detail) of one error category.
// detail, when set, replaces the error's own message (domain errors carry a
// package prefix and are never sent to a client as they are).
type problem struct {
	status int
	slug   string
	title  string
	detail string
}

var internalProblem = problem{status: http.StatusInternalServerError, slug: "internal-error", title: "Internal server error"}

// Problems the idempotency middleware writes itself, before any use case runs.
var (
	idempotencyKeyRequired = problem{status: http.StatusBadRequest, slug: "idempotency-key-required", title: "Idempotency-Key header is required"}
	idempotencyKeyReused   = problem{status: http.StatusUnprocessableEntity, slug: "idempotency-key-reused", title: "Idempotency-Key was already used with a different request"}
)

var (
	invalidSite = problem{http.StatusBadRequest, "invalid-site-id", "Site id is invalid",
		"siteId must be 1..64 characters without whitespace, control characters or '/'"}
	invalidStation = problem{http.StatusBadRequest, "invalid-station-id", "Station id is invalid",
		"stationId must be 1..64 characters without whitespace, control characters or '/'"}
)

// policyDetail is the invalid-policy problem with the detail of one violated
// setting.
func policyDetail(detail string) problem {
	return problem{http.StatusBadRequest, "invalid-policy", "Policy is invalid", detail}
}

// problemCatalogue maps every typed error to its problem, in match order.
// Slugs are the catalogue of .claude/rules/rest-api.md and apis/openapi.yaml.
var problemCatalogue = []struct {
	err error
	p   problem
}{
	{errMalformedRequest, problem{http.StatusBadRequest, "malformed-request", "Malformed request", "request body is not valid JSON for this operation"}},
	{errInvalidLocation, problem{status: http.StatusBadRequest, slug: "malformed-request", title: "Malformed request"}},

	{policy.ErrInvalidSiteID, invalidSite},
	{stationtrip.ErrInvalidStationID, invalidStation},
	{stationtrip.ErrInvalidHintID, problem{http.StatusBadRequest, "invalid-hint-id", "Hint id is invalid",
		"hint id must start with hint- and have no whitespace, control characters or '/'"}},
	{policy.ErrInvalidMaxDetour, policyDetail("maxDetourM must be a finite number greater than 0")},
	{policy.ErrInvalidCandidates, policyDetail("maxCandidates must be between 1 and 5")},
	{policy.ErrInvalidHintTTL, policyDetail("hintTtlSeconds must be greater than 0")},
	{errHintTTLTooLarge, policyDetail("hintTtlSeconds must be at most 2147483647")},
	{policy.ErrInvalidWeight, policyDetail("verticalWeight must be a finite number of at least 0")},
	{policy.ErrInvalidTaskTypes, problem{http.StatusBadRequest, "invalid-task-type", "Eligible task types are invalid",
		"eligibleTaskTypes must be a non-empty subset of PICK, PACK, REBIN, SLAM, RELOCATE, REPLENISH"}},
	{usecases.ErrInvalidCursor, problem{status: http.StatusBadRequest, slug: "invalid-cursor", title: "Cursor is invalid"}},
	{usecases.ErrInvalidLimit, problem{status: http.StatusBadRequest, slug: "invalid-limit", title: "Limit is invalid"}},
	{usecases.ErrInvalidState, problem{status: http.StatusBadRequest, slug: "invalid-state", title: "State is invalid"}},

	{repository.ErrPolicyNotFound, problem{http.StatusNotFound, "policy-not-found", "Interleaving policy not found",
		"the site has not declared an interleaving policy"}},
	{repository.ErrHintNotFound, problem{status: http.StatusNotFound, slug: "hint-not-found", title: "Hint not found"}},
	{repository.ErrStationNotFound, problem{http.StatusNotFound, "station-not-found", "Station not found",
		"no completion has been recorded for the station"}},

	{repository.ErrConcurrentModification, problem{status: http.StatusConflict, slug: "concurrent-modification",
		title: "The resource was modified by another request; re-fetch the latest version and retry"}},
	{usecases.ErrVersionMismatch, problem{status: http.StatusPreconditionFailed, slug: "version-mismatch", title: "The If-Match version is stale"}},

	{usecases.ErrPolicyNotDeclared, problem{http.StatusUnprocessableEntity, "policy-not-declared", "The site has no interleaving policy",
		"declare a policy with PUT /sites/{siteId}/policy before simulating"}},
	{recommend.ErrOriginUnknown, problem{status: http.StatusUnprocessableEntity, slug: "origin-unknown", title: "The origin location has no known geometry"}},
	{usecases.ErrStationPositionUnknown, problem{http.StatusUnprocessableEntity, "station-position-unknown", "The station position is unknown",
		"the station has no recorded position; send fromLocation"}},
}

// problemFor returns the problem of the first catalogue entry err matches, or
// the internal-error problem.
func problemFor(err error) problem {
	for _, entry := range problemCatalogue {
		if errors.Is(err, entry.err) {
			return entry.p
		}
	}
	return internalProblem
}

// problemDetailFor is the detail sent to the client: the problem's fixed text,
// else the error's own message. An unknown origin names the location, as the
// use case wraps it ("recommend: ...: <location>").
func problemDetailFor(err error, p problem) string {
	if errors.Is(err, recommend.ErrOriginUnknown) {
		location := strings.TrimPrefix(err.Error(), recommend.ErrOriginUnknown.Error()+": ")
		if location != "" && location != err.Error() {
			return "no geometry is known for location " + location
		}
		return "the origin location has no known geometry"
	}
	if p.detail != "" {
		return p.detail
	}
	return err.Error()
}
