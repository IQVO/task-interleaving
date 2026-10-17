package system_test

import (
	"regexp"
	"testing"
	"time"

	"github.com/claudioed/task-interleaving/internal/adapters/outbound/system"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
)

func TestRandomIDsAreValidHintIDs(t *testing.T) {
	id := system.RandomIDs{}.NewHintID()
	if !regexp.MustCompile(`^hint-[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(id) {
		t.Fatalf("id = %q", id)
	}
	if _, err := stationtrip.NewHintID(id); err != nil {
		t.Fatalf("id is not a valid hint id: %v", err)
	}
	if id == (system.RandomIDs{}).NewHintID() {
		t.Fatal("ids repeat")
	}
}

func TestClockIsUTCAndCurrent(t *testing.T) {
	before := time.Now().UTC().Add(-time.Second)
	got := system.Clock{}.Now()
	if got.Location() != time.UTC || got.Before(before) || got.After(time.Now().Add(time.Second)) {
		t.Fatalf("Now() = %v", got)
	}
}
