package kafka

import (
	"context"
	"errors"
	"strings"
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

func led(name string) kafkago.Topic {
	return kafkago.Topic{Name: name, Partitions: []kafkago.Partition{{ID: 0, Leader: kafkago.Broker{ID: 1}}}}
}

func TestBrokerCheckPassesWhenEveryTopicHasALeader(t *testing.T) {
	f := &fakeMetadata{resp: &kafkago.MetadataResponse{Topics: []kafkago.Topic{led("a"), led("b")}}}
	if err := NewBrokerCheckWithClient(f, []string{"a", "b"}).Check(context.Background()); err != nil {
		t.Fatalf("check = %v", err)
	}
	if got := f.req.Topics; len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("the request must name exactly the wanted topics, got %v", got)
	}
}

func TestBrokerCheckFailsWhenTheBrokerIsUnreachable(t *testing.T) {
	boom := errors.New("connection refused")
	err := NewBrokerCheckWithClient(&fakeMetadata{err: boom}, []string{"a"}).Check(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("check = %v, want the cause wrapped", err)
	}
}

func TestBrokerCheckNamesEveryProblemTopic(t *testing.T) {
	missing := kafkago.Topic{Name: "missing", Error: kafkago.UnknownTopicOrPartition}
	noLeader := kafkago.Topic{Name: "orphan", Partitions: []kafkago.Partition{{ID: 3, Leader: kafkago.Broker{ID: -1}}}}
	badPartition := kafkago.Topic{Name: "broken", Partitions: []kafkago.Partition{{ID: 1, Error: kafkago.LeaderNotAvailable}}}
	empty := kafkago.Topic{Name: "empty"}
	resp := &kafkago.MetadataResponse{Topics: []kafkago.Topic{led("fine"), missing, noLeader, badPartition, empty}}
	err := NewBrokerCheckWithClient(&fakeMetadata{resp: resp}, []string{"fine", "missing", "orphan", "broken", "empty", "absent"}).Check(context.Background())
	if err == nil {
		t.Fatal("a missing topic must fail the check")
	}
	for _, want := range []string{"missing: ", "orphan: partition 3 has no leader", "broken: partition 1", "empty: no partitions", "absent: not in the broker's answer"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "fine") {
		t.Errorf("a healthy topic must not be reported: %v", err)
	}
}

func TestNewBrokerCheckDoesNotDial(t *testing.T) {
	if NewBrokerCheck([]string{"127.0.0.1:1"}, []string{"a"}) == nil {
		t.Fatal("nil check")
	}
}
