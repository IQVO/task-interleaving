package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/claudioed/task-interleaving/internal/adapters/inbound/expiry"
	outboundkafka "github.com/claudioed/task-interleaving/internal/adapters/outbound/kafka"
	outboxrelay "github.com/claudioed/task-interleaving/internal/adapters/outbound/outbox"
)

// worker is a started background loop: stop cancels its context and done is
// closed once it returned.
type worker struct {
	name string
	stop context.CancelFunc
	done chan struct{}
}

// startOutboxRelay starts the relay goroutine. The relay is not optional: the
// events leave the service only through it, so a missing outbox store or
// missing brokers is a boot error, never a silent degrade. Kafka is dialled
// lazily on the first send, never here.
func startOutboxRelay(store outboxrelay.Store, brokers []string, logger *slog.Logger) (*worker, error) {
	if store == nil {
		return nil, errors.New("outbox relay cannot be built: no outbox store (DATABASE_URL is required: this context runs on real Postgres)")
	}
	if len(brokers) == 0 {
		return nil, errors.New("outbox relay cannot be built: KAFKA_BROKERS is required: this context runs on real Kafka")
	}
	sink := outboundkafka.NewRelaySink(brokers)
	interval := parseRelayInterval(os.Getenv("OUTBOX_RELAY_INTERVAL"), logger)
	maxAttempts := outboxMaxAttempts(os.Getenv("OUTBOX_MAX_ATTEMPTS"), logger)
	relay := outboxrelay.NewRelay(store, sink, logger, outboxrelay.WithInterval(interval), outboxrelay.WithMaxAttempts(maxAttempts))

	return launch("outbox-relay", func(ctx context.Context) {
		defer func() {
			if err := sink.Close(); err != nil {
				logger.Error("kafka relay sink close failed", "error", err)
			}
		}()
		logger.Info("outbox relay running", "interval", interval, "max_attempts", maxAttempts, "brokers", brokers)
		if err := relay.Run(ctx); !errors.Is(err, context.Canceled) {
			logger.Error("outbox relay stopped", "error", err)
		}
	}), nil
}

// startExpiryWorker starts the hint-expiry worker: it calls the ExpireHints
// use case every interval, so an Active hint past its expires_at becomes
// Expired (and its analytics fact is enqueued) even when no claim or
// completion arrives for the station.
func startExpiryWorker(sweeper expiry.Sweeper, interval time.Duration, logger *slog.Logger) *worker {
	w := expiry.NewWorker(sweeper, logger, expiry.WithInterval(interval))
	return launch("hint-expiry", func(ctx context.Context) {
		logger.Info("hint expiry worker running", "interval", interval.String())
		if err := w.Run(ctx); !errors.Is(err, context.Canceled) {
			logger.Error("hint expiry worker stopped", "error", err)
		}
	})
}

// launch runs body in a goroutine under a cancellable context.
func launch(name string, body func(ctx context.Context)) *worker {
	ctx, stop := context.WithCancel(context.Background())
	w := &worker{name: name, stop: stop, done: make(chan struct{})}
	go func() {
		defer close(w.done)
		body(ctx)
	}()
	return w
}
