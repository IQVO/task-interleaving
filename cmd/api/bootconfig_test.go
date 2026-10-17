package main

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// setBootEnv sets a complete, valid mandatory environment; tests then blank
// one variable at a time.
func setBootEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://u:***@127.0.0.1:1/db?sslmode=disable")
	t.Setenv("MIGRATIONS_DATABASE_URL", "")
	t.Setenv("KAFKA_BROKERS", "127.0.0.1:1")
	t.Setenv("HINT_EXPIRY_INTERVAL", "")
	setConsumerEnv(t, "", "")
	t.Setenv("FULFILLMENT_CONSUMER_GROUP", "ful-g")
	t.Setenv("LAYOUT_CONSUMER_GROUP", "lay-g")
}

func TestLoadBootConfigAcceptsACompleteEnvironment(t *testing.T) {
	setBootEnv(t)
	cfg, err := loadBootConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.databaseURL == "" || cfg.migrationsURL != cfg.databaseURL || len(cfg.brokers) != 1 || len(cfg.consumers) != 2 || cfg.expiryEvery <= 0 {
		t.Fatalf("config = %+v", cfg)
	}
	t.Setenv("MIGRATIONS_DATABASE_URL", "postgres://direct")
	if cfg, _ := loadBootConfig(); cfg.migrationsURL != "postgres://direct" {
		t.Fatalf("the direct migrations DSN must win, got %q", cfg.migrationsURL)
	}
}

// Each mandatory variable is a boot error that names it.
func TestLoadBootConfigRefusesEachMissingVariable(t *testing.T) {
	for _, name := range []string{"DATABASE_URL", "KAFKA_BROKERS", "FULFILLMENT_CONSUMER_GROUP", "LAYOUT_CONSUMER_GROUP"} {
		t.Run(name, func(t *testing.T) {
			setBootEnv(t)
			t.Setenv(name, "")
			_, err := loadBootConfig()
			if err == nil || !strings.Contains(err.Error(), name+" is required") {
				t.Fatalf("%s unset = %v, want a boot error naming it", name, err)
			}
		})
	}
}

func TestLoadBootConfigRefusesPermissive(t *testing.T) {
	setBootEnv(t)
	t.Setenv("FULFILLMENT_MODE", "permissive")
	_, err := loadBootConfig()
	if err == nil || !strings.Contains(err.Error(), "FULFILLMENT_MODE=permissive is not supported: this context runs on real Kafka") {
		t.Fatalf("permissive = %v", err)
	}
}

func TestLoadBootConfigRefusesABadExpiryInterval(t *testing.T) {
	setBootEnv(t)
	t.Setenv("HINT_EXPIRY_INTERVAL", "soon")
	_, err := loadBootConfig()
	if err == nil || !strings.Contains(err.Error(), "HINT_EXPIRY_INTERVAL") || !strings.Contains(err.Error(), `"soon"`) {
		t.Fatalf("bad interval = %v, want a boot error naming HINT_EXPIRY_INTERVAL", err)
	}
}

func TestLoadBootConfigReportsEveryMissingVariableAtOnce(t *testing.T) {
	setBootEnv(t)
	t.Setenv("DATABASE_URL", "")
	t.Setenv("KAFKA_BROKERS", " , ")
	_, err := loadBootConfig()
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") || !strings.Contains(err.Error(), "KAFKA_BROKERS") {
		t.Fatalf("both missing = %v", err)
	}
}

func TestBuildAdaptersRefusesAnEmptyDatabaseURL(t *testing.T) {
	_, closeAdapters, err := buildAdapters(context.Background(), "", "", discardLogger())
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL is required") {
		t.Fatalf("empty DATABASE_URL = %v; there is no in-memory fallback", err)
	}
	closeAdapters()
}

// run fails fast on a bad configuration: before it opens a connection and
// before it binds the listener.
func TestRunFailsFastBeforeBindingTheListener(t *testing.T) {
	setBootEnv(t)
	t.Setenv("DATABASE_URL", "")
	addr := freeAddr(t)
	t.Setenv("HTTP_ADDR", addr)

	start := time.Now()
	err := run(t.Context())
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL is required") {
		t.Fatalf("run = %v, want a boot error naming DATABASE_URL", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("a configuration error must not wait on any dependency")
	}
	if c, derr := (&net.Dialer{Timeout: 200 * time.Millisecond}).DialContext(t.Context(), "tcp", addr); derr == nil {
		_ = c.Close()
		t.Fatal("the listener was bound although the configuration is invalid")
	}
}

// A bad HINT_EXPIRY_INTERVAL must not wait for (or hide behind) a database
// dial.
func TestRunRefusesABadExpiryIntervalBeforeDialing(t *testing.T) {
	setBootEnv(t)
	t.Setenv("HINT_EXPIRY_INTERVAL", "soon")
	start := time.Now()
	err := run(t.Context())
	if err == nil || !strings.Contains(err.Error(), "HINT_EXPIRY_INTERVAL") {
		t.Fatalf("run() = %v, want a boot error naming HINT_EXPIRY_INTERVAL", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("the config error took %v: the database was dialled first", time.Since(start))
	}
}

func TestRuntimeTopicsListEveryConsumedAndProducedTopic(t *testing.T) {
	got := strings.Join(runtimeTopics(), ",")
	want := "warehouse.fulfillment.events,warehouse.facility.events," +
		"warehouse.task-interleaving.events,warehouse.task-interleaving.analytics"
	if got != want {
		t.Fatalf("runtime topics = %s, want %s", got, want)
	}
}

func TestLogTopicsNamesWhatTheServiceConsumesAndProduces(t *testing.T) {
	logger, logs := quietLogger()
	logTopics(logger, []string{"b:9092"})
	for _, want := range []string{"warehouse.fulfillment.events", "warehouse.task-interleaving.events", "never creates"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("topics log lacks %q: %s", want, logs.String())
		}
	}
}
