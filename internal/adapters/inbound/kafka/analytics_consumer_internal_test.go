package kafka

import (
	"testing"
	"time"
)

// The skip sampler lets the first call through, then at most one per interval,
// reporting how many were suppressed in between.
func TestSkipSampler(t *testing.T) {
	now := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	s := &skipSampler{every: time.Minute, now: func() time.Time { return now }}

	if emit, suppressed := s.allow(); !emit || suppressed != 0 {
		t.Fatalf("first = %v, %d; want emit, 0", emit, suppressed)
	}
	for range 3 {
		now = now.Add(10 * time.Second)
		if emit, _ := s.allow(); emit {
			t.Fatal("a skip within the interval must be suppressed")
		}
	}
	now = now.Add(time.Minute)
	if emit, suppressed := s.allow(); !emit || suppressed != 3 {
		t.Fatalf("after the interval = %v, %d; want emit, 3 suppressed", emit, suppressed)
	}
}

func TestSanitizeLog(t *testing.T) {
	if got := sanitizeLog("a\r\nb\nc"); got != "abc" {
		t.Fatalf("sanitizeLog = %q, want abc", got)
	}
}
