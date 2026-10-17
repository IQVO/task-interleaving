package kafka

import (
	"context"
	"errors"
	"testing"

	kafkago "github.com/segmentio/kafka-go"
)

type fakeMetadata struct {
	resp *kafkago.MetadataResponse
	err  error
	req  *kafkago.MetadataRequest
}

func (f *fakeMetadata) Metadata(_ context.Context, req *kafkago.MetadataRequest) (*kafkago.MetadataResponse, error) {
	f.req = req
	return f.resp, f.err
}

func answering(topics ...kafkago.Topic) *kafkago.MetadataResponse {
	return &kafkago.MetadataResponse{Brokers: []kafkago.Broker{{ID: 1, Host: "b", Port: 9092}}, Topics: topics}
}

func TestBrokerCheckPassesWhenABrokerAnswers(t *testing.T) {
	f := &fakeMetadata{resp: answering()}
	if err := NewBrokerCheckWithClient(f).Check(context.Background()); err != nil {
		t.Fatalf("check = %v", err)
	}
}

// The request must name no topics: an empty, NON-nil list (a nil list is the
// null that means "all topics").
func TestBrokerCheckAsksForNoTopics(t *testing.T) {
	f := &fakeMetadata{resp: answering()}
	if err := NewBrokerCheckWithClient(f).Check(context.Background()); err != nil {
		t.Fatalf("check = %v", err)
	}
	if f.req == nil || f.req.Topics == nil || len(f.req.Topics) != 0 {
		t.Fatalf("request topics = %#v, want an empty non-nil list", f.req)
	}
}

func TestBrokerCheckFailsWhenTheBrokerIsUnreachable(t *testing.T) {
	boom := errors.New("connection refused")
	err := NewBrokerCheckWithClient(&fakeMetadata{err: boom}).Check(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("check = %v, want the cause wrapped", err)
	}
}

func TestBrokerCheckFailsWhenTheAnswerListsNoBroker(t *testing.T) {
	for name, resp := range map[string]*kafkago.MetadataResponse{
		"nil answer":      nil,
		"no broker given": {},
	} {
		if err := NewBrokerCheckWithClient(&fakeMetadata{resp: resp}).Check(context.Background()); err == nil {
			t.Errorf("%s: a metadata answer without a broker must fail the check", name)
		}
	}
}

// Missing topics are NOT a readiness failure: the broker auto-creates them on
// first write and the consumers retry and log until their producer ran.
func TestBrokerCheckIgnoresMissingAndLeaderlessTopics(t *testing.T) {
	missing := kafkago.Topic{Name: "missing", Error: kafkago.UnknownTopicOrPartition}
	noLeader := kafkago.Topic{Name: "orphan", Partitions: []kafkago.Partition{{ID: 3, Leader: kafkago.Broker{ID: -1}}}}
	empty := kafkago.Topic{Name: "empty"}
	f := &fakeMetadata{resp: answering(missing, noLeader, empty)}
	if err := NewBrokerCheckWithClient(f).Check(context.Background()); err != nil {
		t.Fatalf("topics missing must still be ready, got %v", err)
	}
}

func TestNewBrokerCheckDoesNotDial(t *testing.T) {
	if NewBrokerCheck([]string{"127.0.0.1:1"}) == nil {
		t.Fatal("nil check")
	}
}
