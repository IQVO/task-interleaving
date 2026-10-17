package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	inboundkafka "github.com/claudioed/task-interleaving/internal/adapters/inbound/kafka"
	"github.com/claudioed/task-interleaving/internal/application/usecases"
)

// Consumer modes (FULFILLMENT_MODE, LAYOUT_MODE).
const (
	modePermissive = "permissive"
	modeKafka      = "kafka"
)

// parseConsumerMode validates a *_MODE value (empty and "permissive" = no
// consumer: the local copies are whatever the database holds).
func parseConsumerMode(name, raw string) (string, error) {
	switch raw {
	case "", modePermissive:
		return modePermissive, nil
	case modeKafka:
		return modeKafka, nil
	default:
		return "", fmt.Errorf("unknown %s %q (want kafka or permissive)", name, raw)
	}
}

// consumerSpec is one consumer: its mode and group variables and how to build
// it once both are resolved and the adapters exist.
type consumerSpec struct {
	modeEnv, groupEnv string
	build             func(ad adapters, brokers []string, group string, logger *slog.Logger) *inboundkafka.Consumer
}

// consumerSpecs lists the two consumers (ADR 0003). The group ids come from
// the environment, never from a literal.
func consumerSpecs() []consumerSpec {
	return []consumerSpec{
		{"FULFILLMENT_MODE", "FULFILLMENT_CONSUMER_GROUP", buildFulfillmentConsumer},
		{"LAYOUT_MODE", "LAYOUT_CONSUMER_GROUP", buildLayoutConsumer},
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

// plannedConsumer is a consumer whose mode is kafka, with its resolved group.
type plannedConsumer struct {
	spec  consumerSpec
	group string
}

// planConsumers resolves every consumer's mode and group from the environment
// and refuses to boot on a misconfiguration: an unknown mode, or mode kafka
// without its group variable or without KAFKA_BROKERS. It needs no adapter, so
// run() calls it before any database is dialled.
func planConsumers(specs []consumerSpec, brokers []string) ([]plannedConsumer, error) {
	var planned []plannedConsumer
	for _, spec := range specs {
		mode, err := parseConsumerMode(spec.modeEnv, os.Getenv(spec.modeEnv))
		if err != nil {
			return nil, err
		}
		if mode != modeKafka {
			continue
		}
		group := os.Getenv(spec.groupEnv)
		if group == "" {
			return nil, fmt.Errorf("%s=kafka requires %s", spec.modeEnv, spec.groupEnv)
		}
		planned = append(planned, plannedConsumer{spec: spec, group: group})
	}
	if len(planned) > 0 && len(brokers) == 0 {
		return nil, errors.New("a consumer in kafka mode requires KAFKA_BROKERS")
	}
	return planned, nil
}

// startConsumers starts every planned consumer in its own worker. The readers
// dial lazily inside Run, so a broker outage never blocks boot; each worker
// closes its own reader and dead-letter writer once Run has returned. durable
// reports whether the local copies live in a database: in-memory copies are
// lost on restart while the committed offsets are not, so that combination is
// warned about.
func startConsumers(ad adapters, planned []plannedConsumer, brokers []string, durable bool, logger *slog.Logger) []*worker {
	if len(planned) == 0 {
		logger.Info("no consumer is in kafka mode; the local copies are whatever the database holds")
		return nil
	}
	if !durable {
		logger.Warn("consumers run over in-memory adapters (DATABASE_URL is not set): the local copies are lost on restart while the committed offsets are kept")
	}
	workers := make([]*worker, 0, len(planned))
	for _, p := range planned {
		workers = append(workers, runConsumer(p.spec.build(ad, brokers, p.group, logger), p, brokers, logger))
	}
	return workers
}

// runConsumer runs one consumer in its own goroutine until its worker is
// stopped.
func runConsumer(consumer *inboundkafka.Consumer, p plannedConsumer, brokers []string, logger *slog.Logger) *worker {
	return launch(p.spec.modeEnv, func(ctx context.Context) {
		defer func() {
			if err := consumer.Close(); err != nil {
				logger.Error("consumer close failed", "consumer", consumer.Name, "error", err)
			}
		}()
		logger.Info("consumer running", "consumer", consumer.Name, "topic", consumer.Topic, "group_id", p.group, "brokers", brokers)
		if err := consumer.Run(ctx); !errors.Is(err, context.Canceled) {
			logger.Error("consumer stopped", "consumer", consumer.Name, "error", err)
		}
	})
}
