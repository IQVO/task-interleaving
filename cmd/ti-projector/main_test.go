package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadConfig(t *testing.T) {
	c, err := loadConfig(env(map[string]string{
		"ANALYTICS_DATABASE_URL": "postgres://x", "KAFKA_BROKERS": " a:9092, b:9092 ,",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.group != "task-interleaving-analytics" || c.adminAddr != ":8091" || c.logLevel != "info" ||
		len(c.brokers) != 2 || c.brokers[0] != "a:9092" || c.brokers[1] != "b:9092" {
		t.Fatalf("config = %+v", c)
	}
	c, err = loadConfig(env(map[string]string{
		"ANALYTICS_DATABASE_URL": "postgres://x", "KAFKA_BROKERS": "a:9092",
		"ANALYTICS_CONSUMER_GROUP": "g", "ADMIN_ADDR": ":9", "LOG_LEVEL": "debug",
	}))
	if err != nil || c.group != "g" || c.adminAddr != ":9" || c.logLevel != "debug" {
		t.Fatalf("config = %+v, %v", c, err)
	}
}

func TestLoadConfigNamesWhatIsMissing(t *testing.T) {
	if _, err := loadConfig(env(map[string]string{"KAFKA_BROKERS": "a:9092"})); err != errMissingAnalyticsURL {
		t.Fatalf("err = %v, want %v", err, errMissingAnalyticsURL)
	}
	if _, err := loadConfig(env(map[string]string{"ANALYTICS_DATABASE_URL": "postgres://x", "KAFKA_BROKERS": " , "})); err != errMissingBrokers {
		t.Fatalf("err = %v, want %v", err, errMissingBrokers)
	}
}

func TestAdminMuxFlipsReadinessNotLiveness(t *testing.T) {
	var notReady atomic.Bool
	srv := httptest.NewServer(newAdminMux(&notReady))
	t.Cleanup(srv.Close)
	status := func(path string) (int, string) {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}
	if code, body := status("/healthz"); code != 200 || body != `{"status":"ok"}` {
		t.Fatalf("healthz = %d %s", code, body)
	}
	if code, body := status("/readyz"); code != 200 || body != `{"status":"ready"}` {
		t.Fatalf("readyz = %d %s", code, body)
	}
	notReady.Store(true)
	if code, body := status("/readyz"); code != 503 || body != `{"status":"not_ready"}` {
		t.Fatalf("draining readyz = %d %s", code, body)
	}
	if code, _ := status("/healthz"); code != 200 {
		t.Fatalf("liveness must stay up while draining, got %d", code)
	}
}

// An unreachable analytical database is a boot error after the bounded
// retries, never a silent start.
func TestOpenAnalyticsPoolBadDatabaseFailsBoot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // a cancelled context makes bootretry give up at once
	if _, err := openAnalyticsPool(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), "postgres://u@127.0.0.1:1/db"); err == nil {
		t.Fatal("an unreachable database must fail boot")
	}
}

// An analytics projector whose consumer loop exits must stop, not keep serving
// /readyz (INCIDENTS 2026-10-12): the loop's exit, with or without an error of
// its own, is an error of the process.
func TestAwaitStop_ConsumerLoopExitIsAnError(t *testing.T) {
	never := make(chan struct{})
	for name, loopErr := range map[string]error{"failed": errors.New("commit failed"), "returned nil": nil} {
		t.Run(name, func(t *testing.T) {
			consumerDone := make(chan error, 1)
			consumerDone <- loopErr
			err := awaitStop(never, consumerDone, make(chan error))
			if err == nil {
				t.Fatal("a consumer exit must stop the process with an error")
			}
			if loopErr != nil && !errors.Is(err, loopErr) {
				t.Fatalf("err = %v, want it to wrap %v", err, loopErr)
			}
			if loopErr == nil && !errors.Is(err, errConsumerStopped) {
				t.Fatalf("err = %v, want errConsumerStopped", err)
			}
		})
	}
}

func TestAwaitStop_AdminFailureIsAnErrorAndASignalIsGraceful(t *testing.T) {
	adminErr := make(chan error, 1)
	adminErr <- errors.New("listen tcp: address in use")
	if err := awaitStop(make(chan struct{}), make(chan error), adminErr); err == nil {
		t.Fatal("an admin server failure must stop the process with an error")
	}
	sig := make(chan struct{})
	close(sig)
	if err := awaitStop(sig, make(chan error), make(chan error)); err != nil {
		t.Fatalf("a signal is a graceful stop, got %v", err)
	}
}
