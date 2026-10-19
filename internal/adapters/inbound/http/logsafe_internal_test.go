package http

import "testing"

// A request-derived value can never forge a log line.
func TestLogSafeStripsLineBreaks(t *testing.T) {
	if got := logSafe("/reports/a\r\nERROR forged\nline"); got != "/reports/aERROR forgedline" {
		t.Fatalf("logSafe = %q", got)
	}
	if got := logSafe("/hints/hint-1"); got != "/hints/hint-1" {
		t.Fatalf("logSafe changed a clean value: %q", got)
	}
}
