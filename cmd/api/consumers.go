package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	inboundkafka "github.com/claudioed/task-interleaving/internal/adapters/inbound/kafka"
	"github.com/claudioed/task-interleaving/internal/application/usecases"
)

// Consumer modes (FULFILLMENT_MODE, LAYOUT_MODE). The variables stay (the
// fleet docs and the template rule files name them) but there is one runtime:
// every consumer reads real Kafka. "permissive" used to mean "do not start
// this consumer"; it is rejected at boot.
const (
	modeKafka      = "kafka"
	modePermissive = "permissive"
)

// parseConsumerMode validates a *_MODE value: unset or "kafka" is the one
// supported mode, "permissive" is refused with its own message and anything
// else is unknown.
func parseConsumerMode(name, raw string) (string, error) {
	if raw == "" || raw == modeKafka {
		return modeKafka, nil
	}
	if raw == modePermissive {
		return "", fmt.Errorf("%s=permissive is not supported: this context runs on real Kafka", name)
	}
	return "", fmt.Errorf("unknown %s %q (want kafka)", name, raw)
}

// consumerEnv names the two variables of one consumer.
type consumerEnv struct {
	modeEnv, groupEnv string
}

// consumerEnvs lists the two consumers (ADR 0003). The group ids come from the
// environment, never from a literal.
var consumerEnvs = []consumerEnv{
	{"FULFILLMENT_MODE", "FULFILLMENT_CONSUMER_GROUP"},
	{"LAYOUT_MODE", "LAYOUT_CONSUMER_GROUP"},
}

// plannedConsumer is a consumer with its resolved group.
type plannedConsumer struct {
	env   consumerEnv
	group string
}

// planConsumers resolves every consumer's mode and group from the environment
// and refuses to boot on a misconfiguration: a mode other than kafka, or a
// missing group variable. Every problem is reported, not only the first.
func planConsumers() ([]plannedConsumer, error) {
	planned := make([]plannedConsumer, 0, len(consumerEnvs))
	var errs []error
	for _, env := range consumerEnvs {
		p, err := planConsumer(env)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		planned = append(planned, p)
	}
	return planned, errors.Join(errs...)
}

func planConsumer(env consumerEnv) (plannedConsumer, error) {
	if _, err := parseConsumerMode(env.modeEnv, os.Getenv(env.modeEnv)); err != nil {
		return plannedConsumer{}, err
	}
	group := os.Getenv(env.groupEnv)
	if group == "" {
		return plannedConsumer{}, fmt.Errorf("%s is required: this context runs on real Kafka", env.groupEnv)
	}
	return plannedConsumer{env: env, group: group}, nil
}

// consumerBuilder builds one consumer once its group is resolved.
type consumerBuilder func(brokers []string, group string, logger *slog.Logger) *inboundkafka.Consumer

// consumerBuilders maps each consumer's mode variable to its builder.
func consumerBuilders(ad adapters) map[string]consumerBuilder {
	return map[string]consumerBuilder{
		"FULFILLMENT_MODE": func(brokers []string, group string, logger *slog.Logger) *inboundkafka.Consumer {
			return buildFulfillmentConsumer(ad, brokers, group, logger)
		},
		"LAYOUT_MODE": func(brokers []string, group string, logger *slog.Logger) *inboundkafka.Consumer {
			return buildLayoutConsumer(ad, brokers, group, logger)
		},
	}
}

func buildFulfillmentConsumer(ad adapters, brokers []string, group string, logger *slog.Logger) *inboundkafka.Consumer {
	intake, writer := ad.intake(), ad.tripWriter()
	return inboundkafka.NewFulfillmentConsumer(brokers, group, inboundkafka.FulfillmentUseCases{
		Available: &usecases.ApplyTaskAvailable{Intake: intake, Pool: ad.pool},
		Claimed:   &usecases.ApplyTaskClaimed{Intake: intake, TripWriter: writer, Pool: ad.pool, Clock: ad.clock},
		Completed: &usecases.ApplyTaskCompleted{
			Intake: intake, TripWriter: writer, Policies: ad.policies, Pool: ad.pool,
			Ranker: ad.ranker(), Clock: ad.clock, IDs: ad.ids,
		},
	}, logger)
}

func buildLayoutConsumer(ad adapters, brokers []string, group string, logger *slog.Logger) *inboundkafka.Consumer {
	intake := ad.intake()
	return inboundkafka.NewLayoutConsumer(brokers, group, inboundkafka.LayoutUseCases{
		Zone:           &usecases.ApplyZone{Intake: intake, Book: ad.book},
		Registered:     &usecases.ApplyLocationRegistered{Intake: intake, Book: ad.book},
		Decommissioned: &usecases.ApplyLocationDecommissioned{Intake: intake, Book: ad.book},
		Geometry:       &usecases.ApplyGeometryUpdated{Intake: intake, Book: ad.book},
	}, logger)
}

// startConsumers starts every planned consumer in its own worker. The readers
// dial lazily inside Run, so a broker outage (or a topic that does not exist
// yet) never blocks boot or crashes the process: the consumer logs and
// retries, and /readyz reports not-ready only while the broker is unreachable
// (a missing topic shows as consumer retry logs, never as a readiness
// failure). Each worker closes its own reader and dead-letter writer once Run
// has returned.
func startConsumers(ad adapters, brokers []string, planned []plannedConsumer, logger *slog.Logger) []*worker {
	builders := consumerBuilders(ad)
	workers := make([]*worker, 0, len(planned))
	for _, p := range planned {
		workers = append(workers, runConsumer(builders[p.env.modeEnv](brokers, p.group, logger), p, brokers, logger))
	}
	return workers
}

// consumerRestartDelay is how long a consumer waits before it is run again
// after it stopped on a reader error.
const consumerRestartDelay = time.Second

// runConsumer runs one consumer in its own goroutine until its worker is
// stopped. The consumer is supervised: kafka-go hands a group-join failure
// that outlasted its attempts (an unreachable broker) to FetchMessage, which
// makes Run return; an always-on consumer must not die of that, so it is run
// again after a short delay until the worker is stopped. The reader heals once
// the broker is back and resumes from the committed offset.
func runConsumer(consumer *inboundkafka.Consumer, p plannedConsumer, brokers []string, logger *slog.Logger) *worker {
	return launch(p.env.modeEnv, func(ctx context.Context) {
		defer func() {
			if err := consumer.Close(); err != nil {
				logger.Error("consumer close failed", "consumer", consumer.Name, "error", err)
			}
		}()
		logger.Info("consumer running", "consumer", consumer.Name, "topic", consumer.Topic, "group_id", p.group, "brokers", brokers)
		supervise(ctx, consumer.Run, consumerRestartDelay, func(err error) {
			logger.Error("consumer stopped; restarting", "consumer", consumer.Name, "error", err, "restart_in", consumerRestartDelay.String())
		})
	})
}

// supervise calls run until ctx is cancelled. When run returns for any other
// reason, onStop is told why and run is called again after delay.
func supervise(ctx context.Context, run func(context.Context) error, delay time.Duration, onStop func(error)) {
	for {
		err := run(ctx)
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return
		}
		onStop(err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}
