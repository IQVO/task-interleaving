package ports

import "time"

// Clock is the service's notion of "now". Use cases stamp hints, events and
// policy declarations with it.
type Clock interface {
	Now() time.Time
}

// IDGenerator mints hint ids.
type IDGenerator interface {
	// NewHintID returns a fresh "hint-" id (at most 64 characters, no white
	// space, no '/').
	NewHintID() string
}
