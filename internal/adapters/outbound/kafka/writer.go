// Package kafka is the outbound Kafka adapter. This file holds the one place a
// kafka-go Writer is constructed, so the fleet's acks=all rule (fitness test
// TestKafkaWritersSetRequiredAcks) has a single, obvious home. The transactional
// outbox relay built on top of it sets Message.Topic per message; the Writer
// deliberately carries no Topic (TestKafkaWriterTopicNotSetTwice).
package kafka

import kafkago "github.com/segmentio/kafka-go"

// NewWriter returns the service's synchronous, acks=all Kafka writer.
// Events leave the service only through the transactional outbox relay.
func NewWriter(brokers []string) *kafkago.Writer {
	return &kafkago.Writer{
		Addr:         kafkago.TCP(brokers...),
		Balancer:     &kafkago.Hash{},
		RequiredAcks: kafkago.RequireAll,
	}
}
