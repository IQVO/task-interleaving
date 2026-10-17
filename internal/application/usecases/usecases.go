// Package usecases holds the application's use cases. Every write runs inside
// ONE ports.UnitOfWork: load, apply the aggregate command, save guarded by the
// version that was loaded, and hand the raised facts to the outbox (EventSink).
// The open-task pool, the claims and the location geometry are read from the
// event-fed local copies (ports.TaskPool, ports.LocationBook); nothing here
// calls a sibling context.
package usecases

import (
	"encoding/base64"
	"errors"
)

// Errors of the request validation and of the preconditions that are not the
// aggregates' own. The HTTP adapter maps each to a problem slug.
var (
	// ErrInvalidLimit marks a list limit outside 1..500.
	ErrInvalidLimit = errors.New("limit must be between 1 and 500")
	// ErrInvalidCursor marks a cursor this API did not hand out.
	ErrInvalidCursor = errors.New("cursor is not a value returned by this API")
	// ErrInvalidState marks an unknown hint state filter.
	ErrInvalidState = errors.New("state must be one of Active, Consumed, Expired, Superseded")
	// ErrVersionMismatch: the If-Match version is missing or is not the
	// current version of the policy (HTTP 412).
	ErrVersionMismatch = errors.New("if-match does not match the current version")
	// ErrPolicyNotDeclared: the site has no interleaving policy, so it is
	// inert and a simulation has nothing to apply.
	ErrPolicyNotDeclared = errors.New("the site has no interleaving policy")
	// ErrStationPositionUnknown: the station has no recorded position and no
	// origin location was given.
	ErrStationPositionUnknown = errors.New("the station has no recorded position")
	// ErrInvalidEvent marks a consumed message that can never be applied. It
	// wraps the cause. The consumer logs it and commits past the message.
	ErrInvalidEvent = errors.New("invalid event")
)

// Page bounds (apis/openapi.yaml).
const (
	DefaultListLimit = 100
	MaxListLimit     = 500
)

// DefaultVerticalWeight is the vertical weight of a declared policy that does
// not set one (apis/openapi.yaml).
const DefaultVerticalWeight = 1.0

func listLimit(limit int) (int, error) {
	if limit == 0 {
		return DefaultListLimit, nil
	}
	if limit < 1 || limit > MaxListLimit {
		return 0, ErrInvalidLimit
	}
	return limit, nil
}

// EncodeCursor makes the opaque cursor for "after this key".
func EncodeCursor(key string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(key))
}

// DecodeCursor reverses EncodeCursor; "" is the first page.
func DecodeCursor(cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || len(raw) == 0 {
		return "", ErrInvalidCursor
	}
	return string(raw), nil
}
