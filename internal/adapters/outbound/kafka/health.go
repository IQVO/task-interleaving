package kafka

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	kafkago "github.com/segmentio/kafka-go"
)

// MetadataClient is the subset of *kafkago.Client the readiness check needs,
// so unit tests can substitute a fake.
type MetadataClient interface {
	Metadata(ctx context.Context, req *kafkago.MetadataRequest) (*kafkago.MetadataResponse, error)
}

// BrokerCheck reports whether Kafka is usable by this service: a broker
// answers a metadata request AND every topic the service consumes or
// produces exists with a leader. The service never creates topics (infra owns
// them), so a missing topic is a not-ready state, not something to heal.
//
// The request asks for the named topics only; kafka-go's Client.Metadata does
// not set AllowAutoTopicCreation, so asking never creates a topic.
type BrokerCheck struct {
	client MetadataClient
	topics []string
}

// NewBrokerCheck builds the check over brokers for the given topics. It does
// not dial.
func NewBrokerCheck(brokers, topics []string) *BrokerCheck {
	return NewBrokerCheckWithClient(&kafkago.Client{Addr: kafkago.TCP(brokers...)}, topics)
}

// NewBrokerCheckWithClient builds the check over an explicit client (a fake in
// tests).
func NewBrokerCheckWithClient(c MetadataClient, topics []string) *BrokerCheck {
	return &BrokerCheck{client: c, topics: topics}
}

// Check returns nil when a broker answered and every topic has a leader on
// every partition; otherwise an error naming what is missing. ctx bounds the
// round trip.
func (b *BrokerCheck) Check(ctx context.Context) error {
	resp, err := b.client.Metadata(ctx, &kafkago.MetadataRequest{Topics: b.topics})
	if err != nil {
		return fmt.Errorf("kafka metadata: %w", err)
	}
	if problems := topicProblems(resp, b.topics); len(problems) > 0 {
		return errors.New("kafka topics not ready: " + strings.Join(problems, "; "))
	}
	return nil
}

// topicProblems lists, sorted, what is wrong with each wanted topic in resp.
func topicProblems(resp *kafkago.MetadataResponse, wanted []string) []string {
	byName := make(map[string]kafkago.Topic, len(resp.Topics))
	for _, t := range resp.Topics {
		byName[t.Name] = t
	}
	var problems []string
	for _, name := range wanted {
		t, ok := byName[name]
		if !ok {
			problems = append(problems, name+": not in the broker's answer")
			continue
		}
		if t.Error != nil {
			problems = append(problems, name+": "+t.Error.Error())
			continue
		}
		if p := partitionProblem(t); p != "" {
			problems = append(problems, name+": "+p)
		}
	}
	sort.Strings(problems)
	return problems
}

// partitionProblem is "" when the topic has partitions and each has a leader.
func partitionProblem(t kafkago.Topic) string {
	if len(t.Partitions) == 0 {
		return "no partitions"
	}
	for _, p := range t.Partitions {
		if p.Error != nil {
			return fmt.Sprintf("partition %d: %v", p.ID, p.Error)
		}
		if p.Leader.ID < 0 {
			return fmt.Sprintf("partition %d has no leader", p.ID)
		}
	}
	return ""
}
