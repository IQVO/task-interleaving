// Package system holds the production clock and hint-id generator: the two
// outbound adapters that need no infrastructure. They live apart from
// internal/adapters/outbound/memory so the shipped binary (cmd/**) never
// imports the in-memory test doubles (TestRuntimeBinaryHasNoInMemoryAdapters).
package system

import (
	"crypto/rand"
	"fmt"
	"time"

	"github.com/claudioed/task-interleaving/internal/application/ports"
)

// Compile-time proof that both adapters implement their port.
var (
	_ ports.Clock       = Clock{}
	_ ports.IDGenerator = RandomIDs{}
)

// Clock is the wall clock: the production ports.Clock.
type Clock struct{}

// Now returns the current time in UTC.
func (Clock) Now() time.Time { return time.Now().UTC() }

// RandomIDs is the production ports.IDGenerator: "hint-" followed by a random
// UUID (v4), 41 characters in all.
type RandomIDs struct{}

// NewHintID implements ports.IDGenerator.
func (RandomIDs) NewHintID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Errorf("read random bytes: %w", err))
	}
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // RFC 4122 variant
	return fmt.Sprintf("hint-%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
