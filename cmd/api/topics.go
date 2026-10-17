package main

import (
	"log/slog"

	inboundkafka "github.com/claudioed/task-interleaving/internal/adapters/inbound/kafka"
	"github.com/claudioed/task-interleaving/internal/adapters/outbound/events"
)

// consumedTopics are the topics the consumers read (ADR 0003).
func consumedTopics() []string {
	return []string{inboundkafka.FulfillmentTopic, inboundkafka.LayoutTopic}
}

// producedTopics are the topics the outbox relay writes (ADR 0004).
func producedTopics() []string {
	return []string{events.TopicEvents, events.TopicAnalytics}
}

// runtimeTopics is every topic /readyz requires to exist with a leader. The
// service never creates topics: infrastructure owns them. A topic that is
// missing makes the consumers retry (and log) and the service not-ready; it
// is not a crash loop.
func runtimeTopics() []string {
	return append(consumedTopics(), producedTopics()...)
}

// logTopics records, once at boot, what the service consumes and produces.
func logTopics(logger *slog.Logger, brokers []string) {
	logger.Info("kafka topics (owned by infrastructure; the service never creates them)",
		"brokers", brokers, "consumes", consumedTopics(), "produces", producedTopics())
}
