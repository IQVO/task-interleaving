package main

import (
	"strings"
	"testing"
	"time"
)

// setConsumerEnv sets the mode and group variables of the two consumers.
func setConsumerEnv(t *testing.T, mode, group string) {
	t.Helper()
	for _, name := range []string{"FULFILLMENT", "LAYOUT"} {
		t.Setenv(name+"_MODE", mode)
		t.Setenv(name+"_CONSUMER_GROUP", group)
	}
}

func TestParseConsumerMode(t *testing.T) {
	for raw, want := range map[string]string{"": modePermissive, "permissive": modePermissive, "kafka": modeKafka} {
		got, err := parseConsumerMode("FULFILLMENT_MODE", raw)
		if err != nil || got != want {
			t.Fatalf("parseConsumerMode(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	_, err := parseConsumerMode("FULFILLMENT_MODE", "Kafka")
	if err == nil || !strings.Contains(err.Error(), "FULFILLMENT_MODE") {
		t.Fatalf("an unknown mode must be a boot error naming the variable, got %v", err)
	}
}

// The env var names are part of the deployment contract (the chart and
// .claude/rules/integration-events.md): pin them.
func TestConsumerSpecsPinTheEnvNames(t *testing.T) {
	var got []string
	for _, s := range consumerSpecs() {
		got = append(got, s.modeEnv+":"+s.groupEnv)
	}
	want := "FULFILLMENT_MODE:FULFILLMENT_CONSUMER_GROUP,LAYOUT_MODE:LAYOUT_CONSUMER_GROUP"
	if strings.Join(got, ",") != want {
		t.Fatalf("specs = %v, want %s", got, want)
	}
}

func TestPlanConsumersDefaultsToPermissiveAndNeedsNothing(t *testing.T) {
	setConsumerEnv(t, "", "")
	planned, err := planConsumers(consumerSpecs(), nil)
	if err != nil || len(planned) != 0 {
		t.Fatalf("planned = %v, %v; want nothing and no error without any mode", planned, err)
	}
}

func TestPlanConsumersKafkaModeNeedsItsGroup(t *testing.T) {
	for _, name := range []string{"FULFILLMENT", "LAYOUT"} {
		setConsumerEnv(t, "", "")
		t.Setenv(name+"_MODE", "kafka")
		_, err := planConsumers(consumerSpecs(), []string{"b:9092"})
		if err == nil || !strings.Contains(err.Error(), name+"_CONSUMER_GROUP") {
			t.Fatalf("%s_MODE=kafka without its group = %v, want a boot error naming %s_CONSUMER_GROUP", name, err, name)
		}
	}
}

func TestPlanConsumersKafkaModeNeedsBrokers(t *testing.T) {
	setConsumerEnv(t, "", "")
	t.Setenv("LAYOUT_MODE", "kafka")
	t.Setenv("LAYOUT_CONSUMER_GROUP", "layout-group")
	_, err := planConsumers(consumerSpecs(), nil)
	if err == nil || !strings.Contains(err.Error(), "KAFKA_BROKERS") {
		t.Fatalf("kafka mode without brokers = %v, want a boot error naming KAFKA_BROKERS", err)
	}
}

func TestPlanConsumersRejectsAnUnknownMode(t *testing.T) {
	setConsumerEnv(t, "", "")
	t.Setenv("LAYOUT_MODE", "strict")
	if _, err := planConsumers(consumerSpecs(), []string{"b:9092"}); err == nil {
		t.Fatal("an unknown mode must be a boot error")
	}
}

func TestPlanConsumersResolvesTheGroupsFromTheEnvironment(t *testing.T) {
	setConsumerEnv(t, "kafka", "")
	t.Setenv("FULFILLMENT_CONSUMER_GROUP", "ful-g")
	t.Setenv("LAYOUT_CONSUMER_GROUP", "lay-g")
	planned, err := planConsumers(consumerSpecs(), []string{"b:9092"})
	if err != nil || len(planned) != 2 {
		t.Fatalf("planned = %d, %v; want the two consumers", len(planned), err)
	}
	var groups []string
	for _, p := range planned {
		groups = append(groups, p.group)
	}
	if strings.Join(groups, ",") != "ful-g,lay-g" {
		t.Fatalf("groups = %v", groups)
	}
}

func TestStartConsumersStartsNothingInPermissiveMode(t *testing.T) {
	setConsumerEnv(t, "", "")
	logger, logs := quietLogger()
	if workers := startConsumers(memoryAdapters(), nil, nil, false, logger); len(workers) != 0 {
		t.Fatalf("workers = %d; want none", len(workers))
	}
	if !strings.Contains(logs.String(), "no consumer is in kafka mode") {
		t.Fatalf("the reason must be logged: %s", logs.String())
	}
}

// A misconfiguration is a boot error of run(), reported BEFORE any database is
// dialled.
func TestRunRefusesAConsumerMisconfigurationBeforeDialing(t *testing.T) {
	setConsumerEnv(t, "", "")
	t.Setenv("FULFILLMENT_MODE", "kafka")
	t.Setenv("DATABASE_URL", "postgres://nobody:***@127.0.0.1:1/none?sslmode=disable&connect_timeout=1")
	start := time.Now()
	err := run()
	if err == nil || !strings.Contains(err.Error(), "FULFILLMENT_CONSUMER_GROUP") {
		t.Fatalf("run() = %v, want a boot error naming FULFILLMENT_CONSUMER_GROUP", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("the config error took %v: the database was dialled first", time.Since(start))
	}
}

// Consumers in kafka mode run in their own goroutines and stop, closing their
// reader and dead-letter writer, when their worker is stopped. The broker is
// unreachable: the readers dial lazily, so boot never blocks on Kafka.
func TestStartConsumersRunsAndStopsEveryConsumer(t *testing.T) {
	setConsumerEnv(t, "kafka", "")
	t.Setenv("FULFILLMENT_CONSUMER_GROUP", "ful-g")
	t.Setenv("LAYOUT_CONSUMER_GROUP", "lay-g")
	brokers := []string{"127.0.0.1:1"}
	planned, err := planConsumers(consumerSpecs(), brokers)
	if err != nil {
		t.Fatal(err)
	}
	logger, logs := quietLogger()

	workers := startConsumers(memoryAdapters(), planned, brokers, false, logger)
	if len(workers) != 2 {
		t.Fatalf("workers = %d; want the two consumers", len(workers))
	}
	stopWorkers(logger, workers)
	for _, w := range workers {
		select {
		case <-w.done:
		case <-time.After(10 * time.Second):
			t.Fatalf("worker %s did not stop", w.name)
		}
	}
	for _, want := range []string{"in-memory adapters", "fulfillment consumer", "layout consumer"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs lack %q:\n%s", want, logs.String())
		}
	}
	if strings.Contains(logs.String(), "consumer stopped") || strings.Contains(logs.String(), "close failed") {
		t.Fatalf("a cancelled consumer must stop quietly:\n%s", logs.String())
	}
}
