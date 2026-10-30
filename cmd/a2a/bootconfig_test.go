package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func setBootEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://u:p@127.0.0.1:1/db?sslmode=disable")
	t.Setenv("MIGRATIONS_DATABASE_URL", "")
	t.Setenv("A2A_ADDR", "")
	t.Setenv("A2A_AGENT_URL", testAgentURL)
}

func TestLoadBootConfigAcceptsACompleteEnvironment(t *testing.T) {
	setBootEnv(t)
	cfg, err := loadBootConfig()
	if err != nil || cfg.databaseURL == "" || cfg.agentURL != testAgentURL {
		t.Fatalf("config = %+v, %v", cfg, err)
	}
	if cfg.addr != ":8093" {
		t.Fatalf("A2A_ADDR default = %q, want :8093", cfg.addr)
	}
	if cfg.migrationsURL != cfg.databaseURL {
		t.Fatalf("MIGRATIONS_DATABASE_URL must default to DATABASE_URL, got %q", cfg.migrationsURL)
	}
	t.Setenv("A2A_ADDR", ":9999")
	t.Setenv("MIGRATIONS_DATABASE_URL", "postgres://direct")
	cfg, err = loadBootConfig()
	if err != nil || cfg.addr != ":9999" || cfg.migrationsURL != "postgres://direct" {
		t.Fatalf("overrides not honoured: %+v, %v", cfg, err)
	}
}

// Each missing or rejected value is a boot error that names its variable.
func TestLoadBootConfigRefusesEachMissingOrRejectedValue(t *testing.T) {
	cases := []struct {
		name, variable, value, want string
	}{
		{"database unset", "DATABASE_URL", "", "DATABASE_URL is required: this context runs on real Postgres"},
		{"agent url unset", "A2A_AGENT_URL", "", "A2A_AGENT_URL is required: the Agent Card must advertise"},
		{"agent url blank", "A2A_AGENT_URL", "   ", "A2A_AGENT_URL is required"},
		{"agent url relative", "A2A_AGENT_URL", "/a2a", `A2A_AGENT_URL="/a2a" is not an absolute http(s) URL`},
		{"agent url schemeless", "A2A_AGENT_URL", "task-interleaving-a2a:8093", "is not an absolute http(s) URL"},
		{"agent url wrong scheme", "A2A_AGENT_URL", "ftp://host/", "is not an absolute http(s) URL"},
		{"agent url without host", "A2A_AGENT_URL", "http:///a2a", "is not an absolute http(s) URL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setBootEnv(t)
			t.Setenv(tc.variable, tc.value)
			_, err := loadBootConfig()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s=%q: %v, want a boot error containing %q", tc.variable, tc.value, err, tc.want)
			}
			if !strings.Contains(err.Error(), tc.variable) {
				t.Fatalf("the error does not name %s: %v", tc.variable, err)
			}
		})
	}
}

// run fails fast on a bad configuration: before any connection and before the
// listener binds.
func TestRunFailsFastBeforeBindingTheListener(t *testing.T) {
	setBootEnv(t)
	t.Setenv("A2A_AGENT_URL", "")
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	t.Setenv("A2A_ADDR", addr)

	start := time.Now()
	if err := run(); err == nil || !strings.Contains(err.Error(), "A2A_AGENT_URL is required") {
		t.Fatalf("run = %v, want a boot error naming A2A_AGENT_URL", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("a configuration error must not wait on any dependency")
	}
	if c, derr := (&net.Dialer{Timeout: 200 * time.Millisecond}).DialContext(t.Context(), "tcp", addr); derr == nil {
		_ = c.Close()
		t.Fatal("the listener was bound although the configuration is invalid")
	}
}

// A missing DATABASE_URL is a boot error, never a silent in-memory fallback.
func TestBuildDeps_MissingDatabaseFailsBoot(t *testing.T) {
	_, _, err := buildDeps(context.Background(), quietLogger(), "", "")
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("buildDeps without a database = %v, want an error naming DATABASE_URL", err)
	}
}

// An unreachable database is a boot error after the bounded retries.
func TestBuildDeps_BadDatabaseFailsBoot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // a cancelled context makes bootretry give up at once
	if _, _, err := buildDeps(ctx, quietLogger(), "postgres://u@127.0.0.1:1/db", "postgres://u@127.0.0.1:1/db"); err == nil {
		t.Fatal("an unreachable database must fail boot")
	}
}

// The boot migrations use MIGRATIONS_DATABASE_URL (a direct DSN behind
// PgBouncer), never DATABASE_URL: a malformed migrations DSN must be the
// error, not a dial failure against the runtime DSN.
func TestBuildDeps_RunsMigrationsOnTheMigrationsURL(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := buildDeps(ctx, quietLogger(), "postgres://u@127.0.0.1:1/db", "not-a-valid-connection-string")
	if err == nil || strings.Contains(err.Error(), "refused") {
		t.Fatalf("buildDeps = %v, want the migrations step to fail on its own DSN", err)
	}
}

func TestGetenv(t *testing.T) {
	t.Setenv("TI_A2A_TEST_SET", "v")
	t.Setenv("TI_A2A_TEST_EMPTY", "")
	if got := getenv("TI_A2A_TEST_SET", "d"); got != "v" {
		t.Errorf("set: %q", got)
	}
	if got := getenv("TI_A2A_TEST_EMPTY", "d"); got != "d" {
		t.Errorf("empty: %q", got)
	}
}

func TestNewLoggerLevels(t *testing.T) {
	for level, want := range map[string]slog.Level{"debug": slog.LevelDebug, "WARN": slog.LevelWarn, "warning": slog.LevelWarn, "error": slog.LevelError, "": slog.LevelInfo, "bogus": slog.LevelInfo} {
		if got := newLogger(level); !got.Handler().Enabled(context.Background(), want) || (want > slog.LevelDebug && got.Handler().Enabled(context.Background(), want-1)) {
			t.Errorf("newLogger(%q): level %v not the minimum enabled", level, want)
		}
	}
}
