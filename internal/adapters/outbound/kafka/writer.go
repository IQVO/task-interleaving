// Package kafka is the outbound Kafka adapter. This file holds the one place a
// kafka-go Writer is constructed, so the fleet's acks=all rule (fitness test
// TestKafkaWritersSetRequiredAcks) has a single, obvious home. The transactional
// outbox relay built on top of it sets Message.Topic per message; the Writer
// deliberately carries no Topic (TestKafkaWriterTopicNotSetTwice).
package kafka

import (
	"time"

	kafkago "github.com/segmentio/kafka-go"
)

// writerBatchTimeout is the writer's BatchTimeout. kafka-go's default (1s) caps
// a relay that sends one row per call at about one event a second; 10ms keeps
// writes batched under load while a lone event is flushed at once.
const writerBatchTimeout = 10 * time.Millisecond

// NewWriter returns the service's synchronous, acks=all Kafka writer.
// Events leave the service only through the transactional outbox relay.
func NewWriter(brokers []string) *kafkago.Writer {
	return &kafkago.Writer{
		Addr:         kafkago.TCP(brokers...),
		Balancer:     &kafkago.Hash{},
		RequiredAcks: kafkago.RequireAll,
		BatchTimeout: writerBatchTimeout,
	}
}
