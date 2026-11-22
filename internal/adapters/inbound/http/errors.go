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
	// errUnknownQueryParameter marks a query parameter the operation does not declare.
	errUnknownQueryParameter = errors.New("unknown query parameter")
)

// problem is the fixed (status, slug, title, detail, replay class) of one
// error category. detail, when set, replaces the error's own message (domain
// errors carry a package prefix and are never sent to a client as they are).
// Build one with newProblem only (TestProblemsAreBuiltByTheConstructorOnly): a
// problem cannot exist without a class, and every problem is in the registry
// the tests read.
type problem struct {
	status int
	slug   string
	title  string
	detail string
	class  replayClass
}

// registry holds every problem built by newProblem, in declaration order.
var registry []problem

// newProblem builds and registers a problem. A slug has ONE status and ONE
// class whichever error reaches it: a second registration with another status
// or class is a programming error that stops the process at start (and every
// test of the package).
func newProblem(status int, slug, title, detail string, class replayClass) problem {
	p := problem{status: status, slug: slug, title: title, detail: detail, class: class}
	for _, q := range registry {
		if q.slug != slug {
			continue
		}
		if q.status != status || q.class != class {
			panic("problem " + slug + " is declared twice with another status or replay class")
		}
		return p
	}
	registry = append(registry, p)
	return p
}

var internalProblem = newProblem(http.StatusInternalServerError, "internal-error", "Internal server error", "", replayRetryable)

// Problems the idempotency middleware writes itself, before any use case runs.
// It answers them without storing anything; FINAL records that each is a
// function of the request (and of the stored fingerprint) alone.
var (
	idempotencyKeyRequired = newProblem(http.StatusBadRequest, "idempotency-key-required", "Idempotency-Key header is required", "", replayFinal)
	idempotencyKeyInvalid  = newProblem(http.StatusBadRequest, "idempotency-key-invalid", "Idempotency-Key header is invalid", "", replayFinal)
	idempotencyKeyReused   = newProblem(http.StatusConflict, "idempotency-key-reused", "Idempotency-Key was already used with a different request", "", replayFinal)
)

var (
	invalidSite = newProblem(http.StatusBadRequest, "invalid-site-id", "Site id is invalid",
		"siteId must be 1..64 characters without whitespace, control characters or '/'", replayFinal)
	invalidStation = newProblem(http.StatusBadRequest, "invalid-station-id", "Station id is invalid",
		"stationId must be 1..64 characters without whitespace, control characters or '/'", replayFinal)
	malformedRequest = newProblem(http.StatusBadRequest, "malformed-request", "Malformed request",
		"request body is not valid JSON for this operation", replayFinal)
)

// policyDetail is the invalid-policy problem with the detail of one violated
// setting. The request's own settings alone decide it: FINAL.
func policyDetail(detail string) problem {
	return newProblem(http.StatusBadRequest, "invalid-policy", "Policy is invalid", detail, replayFinal)
}

// problemCatalogue maps every typed error to its problem, in match order.
// Slugs are the catalogue of .claude/rules/rest-api.md and apis/openapi.yaml.
// The replay class of each is declared on its problem, with the reason in ADR
// 0013 (a test holds the ADR table equal to this catalogue).
var problemCatalogue = []struct {
	err error
	p   problem
}{
	{errMalformedRequest, malformedRequest},
	{errInvalidLocation, newProblem(http.StatusBadRequest, "malformed-request", "Malformed request", "", replayFinal)},

	{policy.ErrInvalidSiteID, invalidSite},
	{stationtrip.ErrInvalidStationID, invalidStation},
	{stationtrip.ErrInvalidHintID, newProblem(http.StatusBadRequest, "invalid-hint-id", "Hint id is invalid",
		"hint id must start with hint- and have no whitespace, control characters or '/'", replayFinal)},
	{policy.ErrInvalidMaxDetour, policyDetail("maxDetourM must be a finite number greater than 0")},
	{policy.ErrInvalidCandidates, policyDetail("maxCandidates must be between 1 and 5")},
	{policy.ErrInvalidHintTTL, policyDetail("hintTtlSeconds must be greater than 0")},
	{errHintTTLTooLarge, policyDetail("hintTtlSeconds must be at most 2147483647")},
	{policy.ErrInvalidWeight, policyDetail("verticalWeight must be a finite number of at least 0")},
	{policy.ErrInvalidTaskTypes, newProblem(http.StatusBadRequest, "invalid-task-type", "Eligible task types are invalid",
		"eligibleTaskTypes must be a non-empty subset of PICK, PACK, REBIN, SLAM, RELOCATE, REPLENISH", replayFinal)},
	{errUnknownQueryParameter, newProblem(http.StatusBadRequest, "unknown-query-parameter", "Unknown query parameter", "", replayFinal)},
	{usecases.ErrInvalidCursor, newProblem(http.StatusBadRequest, "invalid-cursor", "Cursor is invalid", "", replayFinal)},
	{usecases.ErrInvalidLimit, newProblem(http.StatusBadRequest, "invalid-limit", "Limit is invalid", "", replayFinal)},
	{usecases.ErrInvalidState, newProblem(http.StatusBadRequest, "invalid-state", "State is invalid", "", replayFinal)},

	{repository.ErrPolicyNotFound, newProblem(http.StatusNotFound, "policy-not-found", "Interleaving policy not found",
		"the site has not declared an interleaving policy", replayRetryable)},
	{repository.ErrHintNotFound, newProblem(http.StatusNotFound, "hint-not-found", "Hint not found", "", replayRetryable)},
	{repository.ErrStationNotFound, newProblem(http.StatusNotFound, "station-not-found", "Station not found",
		"no completion has been recorded for the station", replayRetryable)},

	{repository.ErrConcurrentModification, newProblem(http.StatusConflict, "concurrent-modification",
		"The resource was modified by another request; re-fetch the latest version and retry", "", replayRetryable)},
	{usecases.ErrVersionMismatch, newProblem(http.StatusPreconditionFailed, "version-mismatch", "The If-Match version is stale", "", replayRetryable)},

	{usecases.ErrPolicyNotDeclared, newProblem(http.StatusUnprocessableEntity, "policy-not-declared", "The site has no interleaving policy",
		"declare a policy with PUT /sites/{siteId}/policy before simulating", replayRetryable)},
	{recommend.ErrOriginUnknown, newProblem(http.StatusUnprocessableEntity, "origin-unknown", "The origin location has no known geometry", "", replayRetryable)},
	{usecases.ErrStationPositionUnknown, newProblem(http.StatusUnprocessableEntity, "station-position-unknown", "The station position is unknown",
		"the station has no recorded position; send fromLocation", replayRetryable)},
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
