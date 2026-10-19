//go:build integration

package kafka_test

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	kafkaconsumer "github.com/claudioed/task-interleaving/internal/adapters/inbound/kafka"
	"github.com/claudioed/task-interleaving/internal/adapters/outbound/analyticsstore"
	"github.com/claudioed/task-interleaving/internal/analytics/report"
	"github.com/claudioed/task-interleaving/internal/testing/kafkatest"
	"github.com/claudioed/task-interleaving/internal/testing/pgtest"
)

// The projector's consumer against real infrastructure (testcontainers only):
// CloudEvents on a real topic land in a real analytical Postgres; garbage is
// skipped, poison is dead-lettered byte for byte, a redelivered id is applied
// once, and the offsets are committed. ONE Kafka broker (kafkatest) and ONE
// Postgres (pgtest) per package; every test gets a unique topic and its own
// empty database that this test migrates with the analytical migrations.

func newProjectorStack(t *testing.T) (*analyticsstore.Projection, *analyticsstore.Reader) {
	t.Helper()
	url := pgtest.EmptyURL(t)
	if err := analyticsstore.RunMigrations(url); err != nil {
		t.Fatalf("run analytics migrations: %v", err)
	}
	ctx := context.Background()
	writer, err := analyticsstore.NewPool(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(writer.Close)
	return analyticsstore.NewProjection(writer), analyticsstore.NewReader(writer)
}

func projectorTopic(t *testing.T) string {
	t.Helper()
	topic := kafkatest.Topic(t, "warehouse.task-interleaving.analytics")
	kafkatest.CreateTopic(t, topic+kafkaconsumer.DLQSuffix)
	return topic
}

func adoptionTotals(t *testing.T, r *analyticsstore.Reader, from, to time.Time, site string) (issued, consumed, expired int) {
	t.Helper()
	days, err := r.AdoptionDays(context.Background(), report.AdoptionQuery{
		Range: report.Range{From: from, To: to}, SiteID: site, Granularity: report.GranularityDay,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range days {
		issued += d.Issued
		consumed += d.Consumed
		expired += d.Expired
	}
	return issued, consumed, expired
}

func TestAnalyticsConsumer_RealKafkaAndPostgres(t *testing.T) {
	brokers := kafkatest.Brokers(t)
	projection, reader := newProjectorStack(t)
	topic := projectorTopic(t)
	group := fmt.Sprintf("task-interleaving-analytics-itest-%d", time.Now().UnixNano())
	now := time.Now().UTC().Truncate(time.Second)

	c := kafkaconsumer.NewAnalyticsConsumer(brokers, topic, group, projection, quietLogger())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = c.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
		_ = c.Close()
	})

	stamp := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }
	msg := func(id, entity, name, subject, when, data string) kafkatest.Message {
		raw := ce(id, analyticsSource, "com.warehouse.wes.task-interleaving."+entity+"."+name, subject,
			"urn:warehouse:task-interleaving:analytics:"+name+":v1", data)
		// ce() stamps a fixed time; re-stamp it so the report window can be the test's.
		raw = bytes.Replace(raw, []byte(`"time":"2026-10-17T09:00:00Z"`), []byte(`"time":"`+when+`"`), 1)
		return kafkatest.Message{Key: subject, Value: raw}
	}
	issued := func(id, hint string, d time.Duration) kafkatest.Message {
		return msg(id, "stationtrip", "InterleavingHintIssued", "STN-IT", stamp(d),
			`{"hint_id":"`+hint+`","station_id":"STN-IT","site_id":"SITE-IT","after_task_id":"t0","origin_location":"A-01","expires_at":"`+stamp(d+90*time.Second)+`","policy_version":1,"candidates":[{"rank":1,"task_id":"t1","task_type":"PICK","location":"B-01","detour_m":4}]}`)
	}
	consumed := func(id, hint string, d time.Duration) kafkatest.Message {
		return msg(id, "stationtrip", "InterleavingHintConsumed", "STN-IT", stamp(d),
			`{"hint_id":"`+hint+`","station_id":"STN-IT","site_id":"SITE-IT","task_id":"t1","rank":1,"detour_m":4,"consumed_at":"`+stamp(d)+`"}`)
	}
	expired := func(id, hint string, d time.Duration) kafkatest.Message {
		return msg(id, "stationtrip", "InterleavingHintExpired", "STN-IT", stamp(d),
			`{"hint_id":"`+hint+`","station_id":"STN-IT","site_id":"SITE-IT","expires_at":"`+stamp(d)+`"}`)
	}

	// Three hints: one consumed 20 s after issue, one expired, one superseded
	// (issued, no ending event); plus a policy declaration.
	kafkatest.Produce(t, topic,
		issued("evt-i1", "hint-1", 0),
		consumed("evt-c1", "hint-1", 20*time.Second),
		issued("evt-i2", "hint-2", time.Minute),
		expired("evt-x2", "hint-2", time.Minute+90*time.Second),
		issued("evt-i3", "hint-3", 4*time.Minute),
		msg("evt-p1", "policy", "InterleavingPolicyDeclared", "SITE-IT", stamp(-time.Minute),
			`{"site_id":"SITE-IT","max_detour_m":25,"max_candidates":3,"hint_ttl_seconds":90,"eligible_task_types":["PICK"],"vertical_weight":1,"version":1,"declared_at":"`+stamp(-time.Minute)+`"}`),
	)

	lo, hi := now.Add(-time.Hour), now.Add(time.Hour)
	eventually(t, "the adoption is projected", func() bool {
		i, c, x := adoptionTotals(t, reader, lo, hi, "")
		return i == 3 && c == 1 && x == 1
	})
	if i, _, _ := adoptionTotals(t, reader, lo, hi, "SITE-OTHER"); i != 0 {
		t.Fatalf("another site's adoption = %d issued, want 0", i)
	}

	// A replay of the same id changes nothing (the store dedupes).
	kafkatest.Produce(t, topic, issued("evt-i1", "hint-1", 0))

	// Garbage is skipped and the partition moves on; poison (payload that
	// contradicts the subject) is dead-lettered byte for byte and the
	// partition moves on too.
	kafkatest.Produce(t, topic, kafkatest.Message{Key: "STN-IT", Value: []byte(`{"not":"cloudevents"}`)})
	poison := msg("evt-bad", "stationtrip", "InterleavingHintExpired", "STN-OTHER", stamp(0),
		`{"hint_id":"hint-9","station_id":"STN-IT","site_id":"SITE-IT","expires_at":"`+stamp(0)+`"}`)
	kafkatest.Produce(t, topic, poison)
	kafkatest.Produce(t, topic, issued("evt-i4", "hint-4", 5*time.Minute))
	eventually(t, "the message after the poison is projected", func() bool {
		i, _, _ := adoptionTotals(t, reader, lo, hi, "")
		return i == 4
	})
	dlq := kafkatest.ReadN(t, topic+kafkaconsumer.DLQSuffix, 1, 30*time.Second)
	if len(dlq) != 1 || !bytes.Equal(dlq[0].Value, poison.Value) {
		t.Fatalf("dead-lettered = %+v, want the poison bytes %s", dlq, poison.Value)
	}
	if i, _, _ := adoptionTotals(t, reader, lo, hi, ""); i != 4 {
		t.Fatalf("issued after the replay = %d, want 4 (the replay of evt-i1 counted once)", i)
	}

	// Freshness: the newest applied event time.
	asOf, err := reader.LastEventAt(context.Background())
	if err != nil || asOf == nil || !asOf.Equal(now.Add(5*time.Minute)) {
		t.Fatalf("freshness = %v, %v; want %v", asOf, err, now.Add(5*time.Minute))
	}
}
