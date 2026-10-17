// Command api is task-interleaving's composition root: it wires env config
// into adapters, adapters into use cases, and use cases into the HTTP router,
// the hint-expiry worker and the outbox relay. Nothing else in the module
// imports it.
//
// Environment:
//
//	HTTP_ADDR                 listen address (default :8080)
//	DATABASE_URL              Postgres DSN; unset = in-memory adapters (development)
//	MIGRATIONS_DATABASE_URL   direct DSN for the boot migrations (default DATABASE_URL)
//	KAFKA_BROKERS             comma-separated brokers; the outbox relay runs when this
//	                          is set (and DATABASE_URL is); required by any *_MODE=kafka
//	OUTBOX_RELAY_INTERVAL     relay poll interval (Go duration or seconds, default 1s)
//	OUTBOX_MAX_ATTEMPTS       failures before the relay excludes a row (default 20, 0 = unlimited)
//	FULFILLMENT_MODE, LAYOUT_MODE
//	                          kafka | permissive (default permissive = consumer not started)
//	FULFILLMENT_CONSUMER_GROUP, LAYOUT_CONSUMER_GROUP
//	                          stable group ids; required when the mode is kafka
//	                          (fulfillment-execution's TaskAvailable, TaskClaimed and
//	                          TaskCompleted; facility-layout's zone, location and
//	                          geometry events; ADR 0003). A bad mode, a missing group
//	                          or missing KAFKA_BROKERS is a boot error
//	HINT_EXPIRY_INTERVAL      how often hints past their expires_at are expired
//	                          (Go duration, default 5s; a bad value is a boot error)
//	SHUTDOWN_DRAIN_DELAY      wait after flipping /readyz before closing (default 5s)
//	LOG_LEVEL                 debug | info | warn | error (default info)
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	inboundhttp "github.com/claudioed/task-interleaving/internal/adapters/inbound/http"
)

// shutdownTimeout bounds the HTTP server's graceful drain.
const shutdownTimeout = 10 * time.Second

// workerDrainTimeout bounds how long shutdown waits for each background
// worker to return after its context is cancelled.
const workerDrainTimeout = 10 * time.Second

func main() {
	if err := run(); err != nil {
		slog.Error("service exited with error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	logger := newLogger(getenv("LOG_LEVEL", "info"))
	slog.SetDefault(logger)

	// Configuration errors come first: a bad HINT_EXPIRY_INTERVAL must not
	// wait for (or hide behind) a database dial.
	expiryEvery, err := hintExpiryInterval(os.Getenv("HINT_EXPIRY_INTERVAL"))
	if err != nil {
		return err
	}
	brokers := brokersFromEnv()
	planned, err := planConsumers(consumerSpecs(), brokers)
	if err != nil {
		return err
	}

	databaseURL := os.Getenv("DATABASE_URL")
	// closeAdapters (pool.Close) is deferred FIRST so it runs LAST, after the
	// HTTP drain and the workers have stopped.
	ad, closeAdapters, err := buildAdapters(context.Background(), databaseURL, migrationsDatabaseURL(databaseURL), logger)
	if err != nil {
		return err
	}
	defer closeAdapters()

	readiness := &inboundhttp.Readiness{}
	httpServer := &http.Server{
		Addr:              getenv("HTTP_ADDR", ":8080"),
		Handler:           inboundhttp.NewRouter(buildServer(ad, readiness)),
		ReadHeaderTimeout: 5 * time.Second,
	}
	// Stop order: the consumers go first (they enqueue outbox rows while they
	// drain), then the expiry worker (it enqueues rows too), and the relay
	// last so nothing committed before it is stranded.
	workers := startConsumers(ad, planned, brokers, databaseURL != "", logger)
	workers = append(workers,
		startExpiryWorker(ad.expireHints(), expiryEvery, logger),
		startOutboxRelay(ad.outbox, brokers, logger),
	)
	return serveUntilSignal(logger, httpServer, readiness, workers)
}

// serveUntilSignal runs httpServer until SIGINT/SIGTERM (or a listen error),
// then shuts down in the fleet order: (1) /readyz flips to 503, (2) wait the
// drain delay, (3) drain HTTP, (4) stop and await the workers in the order
// given (nil entries are workers that are off); the caller's deferred
// pool.Close runs after all of it.
func serveUntilSignal(logger *slog.Logger, httpServer *http.Server, readiness *inboundhttp.Readiness, workers []*worker) error {
	errCh := make(chan error, 1)
	go func() {
		logger.Info("http server listening", "addr", httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	select {
	case err := <-errCh:
		stopWorkers(logger, workers)
		return err
	case <-ctx.Done():
	}

	readiness.SetNotReady()
	if delay := shutdownDrainDelay(logger); delay > 0 {
		logger.Info("shutdown: readiness flipped to not-ready; waiting for traffic to drain", "drain_delay", delay.String())
		time.Sleep(delay)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	err := httpServer.Shutdown(shutdownCtx)

	stopWorkers(logger, workers)
	return err
}

// stopWorkers stops and awaits each worker in turn, in the order given.
func stopWorkers(logger *slog.Logger, workers []*worker) {
	for _, w := range workers {
		if w == nil {
			continue
		}
		w.stop()
		await(logger, w, workerDrainTimeout)
	}
}

// await waits (bounded by timeout) for w to return; nil w is a no-op.
func await(logger *slog.Logger, w *worker, timeout time.Duration) {
	if w == nil {
		return
	}
	select {
	case <-w.done:
	case <-time.After(timeout):
		logger.Warn("worker did not stop before the shutdown drain deadline", "worker", w.name)
	}
}
