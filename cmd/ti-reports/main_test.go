package main

import (
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestRunWithoutAnalyticsURLFailsBoot(t *testing.T) {
	t.Setenv("ANALYTICS_DATABASE_URL", "")
	err := run()
	if err == nil || !strings.Contains(err.Error(), "ANALYTICS_DATABASE_URL") {
		t.Fatalf("run() = %v, want a boot error naming ANALYTICS_DATABASE_URL", err)
	}
}

func TestGetenv(t *testing.T) {
	t.Setenv("TI_REPORTS_TEST_SET", "v")
	t.Setenv("TI_REPORTS_TEST_EMPTY", "")
	if got := getenv("TI_REPORTS_TEST_SET", "d"); got != "v" {
		t.Errorf("set: %q", got)
	}
	if got := getenv("TI_REPORTS_TEST_EMPTY", "d"); got != "d" {
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
