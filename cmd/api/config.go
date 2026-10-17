package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/claudioed/task-interleaving/internal/adapters/inbound/expiry"
	outboxrelay "github.com/claudioed/task-interleaving/internal/adapters/outbound/outbox"
)

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// newLogger builds the JSON logger; LOG_LEVEL debug|info|warn|warning|error
// (case-insensitive; "warning" is accepted like "warn" fleet-wide).
func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}

// hintExpiryInterval reads HINT_EXPIRY_INTERVAL: how often the expiry worker
// sweeps for hints past their expires_at. Unset = expiry.DefaultInterval (5s).
// Unlike the relay knobs a bad value is a BOOT ERROR, not a silent default: a
// typo that quietly slowed expiry down would leave stale hints Active.
func hintExpiryInterval(raw string) (time.Duration, error) {
	if strings.TrimSpace(raw) == "" {
		return expiry.DefaultInterval, nil
	}
	d, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("HINT_EXPIRY_INTERVAL %q is not a valid duration (use a Go duration such as 5s or 500ms): %w", raw, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("HINT_EXPIRY_INTERVAL %q must be greater than zero (use a Go duration such as 5s or 500ms)", raw)
	}
	return d, nil
}

// parseRelayInterval reads OUTBOX_RELAY_INTERVAL (Go duration or plain
// seconds), defaulting when unset, malformed or non-positive.
func parseRelayInterval(raw string, logger *slog.Logger) time.Duration {
	if raw == "" {
		return outboxrelay.DefaultInterval
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		if secs, serr := strconv.ParseFloat(raw, 64); serr == nil {
			d, err = time.Duration(secs*float64(time.Second)), nil
		}
	}
	if err != nil || d <= 0 {
		logger.Warn("invalid OUTBOX_RELAY_INTERVAL; using the default", "value", raw, "default", outboxrelay.DefaultInterval)
		return outboxrelay.DefaultInterval
	}
	return d
}

// outboxMaxAttempts reads OUTBOX_MAX_ATTEMPTS: how many times an outbox row
// may fail before the relay excludes it from claims. Unset = the fleet default
// (20); 0 = unlimited; negative or unparsable values log a warning and use the
// default, so a typo never silently removes the cap.
func outboxMaxAttempts(raw string, logger *slog.Logger) int {
	if raw == "" {
		return outboxrelay.DefaultMaxAttempts
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 0 {
		logger.Warn("invalid OUTBOX_MAX_ATTEMPTS; using the default", "value", raw, "default", outboxrelay.DefaultMaxAttempts)
		return outboxrelay.DefaultMaxAttempts
	}
	return n
}

// DefaultShutdownDrainDelay is how long shutdown waits, after flipping
// /readyz to not-ready, before closing the listener (SHUTDOWN_DRAIN_DELAY
// overrides it; "0" disables it).
const DefaultShutdownDrainDelay = 5 * time.Second

// shutdownDrainDelay reads SHUTDOWN_DRAIN_DELAY ("0" disables; unset,
// negative or unparsable = the default).
func shutdownDrainDelay(logger *slog.Logger) time.Duration {
	raw := os.Getenv("SHUTDOWN_DRAIN_DELAY")
	if raw == "" {
		return DefaultShutdownDrainDelay
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		logger.Warn("ignoring invalid SHUTDOWN_DRAIN_DELAY", "value", raw, "default", DefaultShutdownDrainDelay.String())
		return DefaultShutdownDrainDelay
	}
	return d
}

// brokersFromEnv reads KAFKA_BROKERS (comma separated); nil when unset.
func brokersFromEnv() []string {
	var brokers []string
	for _, b := range strings.Split(os.Getenv("KAFKA_BROKERS"), ",") {
		if b = strings.TrimSpace(b); b != "" {
			brokers = append(brokers, b)
		}
	}
	return brokers
}

// bootConfig is the mandatory configuration of the service: real Postgres,
// real Kafka, the two consumers' groups and the expiry interval. There is no
// default and no degraded mode behind any of the first three.
type bootConfig struct {
	databaseURL   string
	migrationsURL string
	brokers       []string
	consumers     []plannedConsumer
	expiryEvery   time.Duration
}

// loadBootConfig reads and validates the whole environment. Every problem is
// reported at once, each naming its variable, so the process fails fast at
// boot, before it opens a connection or binds the listener.
func loadBootConfig() (bootConfig, error) {
	cfg := bootConfig{databaseURL: os.Getenv("DATABASE_URL"), brokers: brokersFromEnv()}
	cfg.migrationsURL = migrationsDatabaseURL(cfg.databaseURL)
	var errs []error
	if cfg.databaseURL == "" {
		errs = append(errs, errNoDatabase)
	}
	if len(cfg.brokers) == 0 {
		errs = append(errs, errors.New("KAFKA_BROKERS is required: this context runs on real Kafka"))
	}
	consumers, err := planConsumers()
	if err != nil {
		errs = append(errs, err)
	}
	cfg.consumers = consumers
	expiryEvery, err := hintExpiryInterval(os.Getenv("HINT_EXPIRY_INTERVAL"))
	if err != nil {
		errs = append(errs, err)
	}
	cfg.expiryEvery = expiryEvery
	if err := errors.Join(errs...); err != nil {
		return bootConfig{}, fmt.Errorf("invalid configuration: %w", err)
	}
	return cfg, nil
}
