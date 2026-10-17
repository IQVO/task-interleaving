package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/claudioed/task-interleaving/internal/application/outbox"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// lockedBuffer is a log sink safe for the worker goroutines.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func quietLogger() (*slog.Logger, *lockedBuffer) {
	logs := &lockedBuffer{}
	return slog.New(slog.NewTextHandler(logs, nil)), logs
}

// fakeStore is a relay Store that never has anything to send.
type fakeStore struct {
	mu     sync.Mutex
	drains int
}

func (s *fakeStore) Drain(ctx context.Context, _, _ int, _ func(context.Context, outbox.Message) error) (int, error) {
	s.mu.Lock()
	s.drains++
	s.mu.Unlock()
	return 0, ctx.Err()
}

func (s *fakeStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.drains
}

// The relay is not optional: without a store or without brokers the service
// refuses to boot instead of degrading silently.
func TestOutboxRelayRefusesToBuildWithoutAStoreOrBrokers(t *testing.T) {
	logger := discardLogger()
	w, err := startOutboxRelay(nil, []string{"b:9092"}, logger)
	if err == nil || w != nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("no store = %v, %v; want a boot error naming DATABASE_URL", w, err)
	}
	w, err = startOutboxRelay(&fakeStore{}, nil, logger)
	if err == nil || w != nil || !strings.Contains(err.Error(), "KAFKA_BROKERS") {
		t.Fatalf("no brokers = %v, %v; want a boot error naming KAFKA_BROKERS", w, err)
	}
}

func TestOutboxRelayRunsAndStops(t *testing.T) {
	t.Setenv("OUTBOX_RELAY_INTERVAL", "5ms")
	t.Setenv("OUTBOX_MAX_ATTEMPTS", "3")
	store := &fakeStore{}
	w, err := startOutboxRelay(store, []string{"127.0.0.1:1"}, discardLogger())
	if err != nil || w == nil {
		t.Fatalf("a store and brokers must start the relay: %v, %v", w, err)
	}
	waitUntil(t, func() bool { return store.count() >= 2 }, "the relay never polled twice")
	w.stop()
	select {
	case <-w.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the relay did not stop")
	}
}

// fakeSweeper counts the sweeps the expiry worker asks for.
type fakeSweeper struct {
	mu     sync.Mutex
	sweeps int
}

func (s *fakeSweeper) Handle(context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweeps++
	return 0, nil
}

func (s *fakeSweeper) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sweeps
}

func TestExpiryWorkerSweepsOnItsIntervalAndStops(t *testing.T) {
	sweeper := &fakeSweeper{}
	w := startExpiryWorker(sweeper, 5*time.Millisecond, discardLogger())
	waitUntil(t, func() bool { return sweeper.count() >= 3 }, "the expiry worker never swept three times")
	w.stop()
	select {
	case <-w.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the expiry worker did not stop")
	}
}

func waitUntil(t *testing.T, cond func() bool, failure string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatal(failure)
}

func TestAwait(t *testing.T) {
	logger, logs := quietLogger()
	await(logger, nil, time.Millisecond) // nil worker: no-op
	done := &worker{name: "done", done: make(chan struct{})}
	close(done.done)
	await(logger, done, time.Second)
	if logs.String() != "" {
		t.Fatalf("a stopped worker must not warn: %s", logs.String())
	}
	await(logger, &worker{name: "stuck", done: make(chan struct{})}, 10*time.Millisecond)
	if !strings.Contains(logs.String(), "worker did not stop") || !strings.Contains(logs.String(), "stuck") {
		t.Fatalf("a stuck worker must be reported: %s", logs.String())
	}
}
