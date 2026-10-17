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

// logTopics records, once at boot, what the service consumes and produces.
// It is informational only: topics play no part in /readyz. The cluster broker
// auto-creates a topic on first write (the relay's writer sets
// AllowAutoTopicCreation), a consumed topic exists once its producer ran, and
// until then the consumer retries and logs. The service never creates a topic
// through an admin API.
func logTopics(logger *slog.Logger, brokers []string) {
	logger.Info("kafka topics (informational; the broker auto-creates them on first write, the service never creates them through an admin API)",
		"brokers", brokers, "consumes", consumedTopics(), "produces", producedTopics())
}
