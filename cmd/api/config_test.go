package main

import (
	"strings"
	"testing"
	"time"

	"github.com/claudioed/task-interleaving/internal/adapters/inbound/expiry"
	outboxrelay "github.com/claudioed/task-interleaving/internal/adapters/outbound/outbox"
)

func TestHintExpiryInterval(t *testing.T) {
	for raw, want := range map[string]time.Duration{
		"": expiry.DefaultInterval, "  ": expiry.DefaultInterval, "5s": 5 * time.Second, "250ms": 250 * time.Millisecond, " 2m ": 2 * time.Minute,
	} {
		got, err := hintExpiryInterval(raw)
		if err != nil || got != want {
			t.Errorf("%q = %v %v, want %v", raw, got, err, want)
		}
	}
	for _, raw := range []string{"soon", "5", "5 seconds", "-1s", "0s", "0"} {
		_, err := hintExpiryInterval(raw)
		if err == nil || !strings.Contains(err.Error(), "HINT_EXPIRY_INTERVAL") || !strings.Contains(err.Error(), raw) {
			t.Errorf("%q: error %v must name the variable and the value", raw, err)
		}
	}
	if expiry.DefaultInterval != 5*time.Second {
		t.Fatalf("default = %v, want 5s", expiry.DefaultInterval)
	}
}

func TestOutboxMaxAttemptsDefaultsToTheFleetCap(t *testing.T) {
	logger, logs := quietLogger()
	for _, tc := range []struct {
		raw  string
		want int
	}{{"", outboxrelay.DefaultMaxAttempts}, {"7", 7}, {"0", 0}, {" 3 ", 3}} {
		if got := outboxMaxAttempts(tc.raw, logger); got != tc.want {
			t.Errorf("%q = %d, want %d", tc.raw, got, tc.want)
		}
	}
	if logs.String() != "" {
		t.Fatalf("valid values must not warn: %s", logs.String())
	}
	for _, raw := range []string{"-1", "many", "1.5"} {
		if got := outboxMaxAttempts(raw, logger); got != outboxrelay.DefaultMaxAttempts {
			t.Errorf("%q = %d, want the default so a typo never removes the cap", raw, got)
		}
	}
	if !strings.Contains(logs.String(), "invalid OUTBOX_MAX_ATTEMPTS") {
		t.Fatalf("a bad value must be warned about: %s", logs.String())
	}
}

func TestParseRelayInterval(t *testing.T) {
	logger, _ := quietLogger()
	for raw, want := range map[string]time.Duration{
		"": outboxrelay.DefaultInterval, "250ms": 250 * time.Millisecond, "2": 2 * time.Second, "0.5": 500 * time.Millisecond,
		"nope": outboxrelay.DefaultInterval, "-1s": outboxrelay.DefaultInterval, "0": outboxrelay.DefaultInterval,
	} {
		if got := parseRelayInterval(raw, logger); got != want {
			t.Errorf("%q = %v, want %v", raw, got, want)
		}
	}
}

func TestShutdownDrainDelay(t *testing.T) {
	logger, _ := quietLogger()
	for raw, want := range map[string]time.Duration{
		"": DefaultShutdownDrainDelay, "0": 0, "2s": 2 * time.Second, "-1s": DefaultShutdownDrainDelay, "x": DefaultShutdownDrainDelay,
	} {
		t.Setenv("SHUTDOWN_DRAIN_DELAY", raw)
		if got := shutdownDrainDelay(logger); got != want {
			t.Errorf("%q = %v, want %v", raw, got, want)
		}
	}
}

func TestBrokersAndMigrationsEnv(t *testing.T) {
	t.Setenv("KAFKA_BROKERS", " a:9092, ,b:9092 ")
	if got := strings.Join(brokersFromEnv(), "|"); got != "a:9092|b:9092" {
		t.Fatalf("brokers = %q", got)
	}
	t.Setenv("KAFKA_BROKERS", "")
	if got := brokersFromEnv(); got != nil {
		t.Fatalf("no brokers = %v", got)
	}
	t.Setenv("MIGRATIONS_DATABASE_URL", "")
	if got := migrationsDatabaseURL("postgres://app"); got != "postgres://app" {
		t.Fatalf("falls back to DATABASE_URL: %q", got)
	}
	t.Setenv("MIGRATIONS_DATABASE_URL", "postgres://direct")
	if got := migrationsDatabaseURL("postgres://app"); got != "postgres://direct" {
		t.Fatalf("MIGRATIONS_DATABASE_URL wins: %q", got)
	}
}

func TestNewLoggerLevels(t *testing.T) {
	for level, enabled := range map[string]struct{ debug, warn, info bool }{
		"debug": {true, true, true}, "info": {false, true, true}, "": {false, true, true},
		"WARN": {false, true, false}, "warning": {false, true, false}, "error": {false, false, false},
	} {
		l := newLogger(level)
		ctx := t.Context()
		if l.Enabled(ctx, -4) != enabled.debug || l.Enabled(ctx, 4) != enabled.warn || l.Enabled(ctx, 0) != enabled.info {
			t.Errorf("LOG_LEVEL=%q enables debug/info/warn wrongly", level)
		}
	}
}
