package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// An always-on consumer is run again after it stopped on a reader error (an
// unreachable broker), and stops for good only when its context is cancelled.
func TestSuperviseRunsAgainAfterAReaderErrorUntilCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runs, stops := 0, 0
	boom := errors.New("failed to dial")
	done := make(chan struct{})
	go func() {
		defer close(done)
		supervise(ctx, func(ctx context.Context) error {
			runs++
			if runs == 3 {
				cancel()
				return ctx.Err()
			}
			return boom
		}, time.Millisecond, func(err error) {
			stops++
			if !errors.Is(err, boom) {
				t.Errorf("onStop got %v", err)
			}
		})
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("supervise did not return after the context was cancelled")
	}
	if runs != 3 || stops != 2 {
		t.Fatalf("runs = %d, stops = %d; want 3 runs and 2 reported stops", runs, stops)
	}
}

// A run that ends because the context is cancelled is a clean stop: no
// restart and nothing reported.
func TestSuperviseStopsQuietlyWhenTheContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	supervise(ctx, func(ctx context.Context) error { return ctx.Err() }, time.Hour, func(err error) {
		t.Errorf("a cancelled consumer must not be reported: %v", err)
	})
}

// The wait between two runs is cut short by the cancellation.
func TestSuperviseDoesNotWaitOutTheDelayWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		supervise(ctx, func(context.Context) error { return errors.New("down") }, time.Hour, func(error) { cancel() })
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("supervise sat out the restart delay after the cancellation")
	}
}

// setConsumerEnv sets the mode and group variables of the two consumers.
func setConsumerEnv(t *testing.T, mode, group string) {
	t.Helper()
	for _, name := range []string{"FULFILLMENT", "LAYOUT"} {
		t.Setenv(name+"_MODE", mode)
		t.Setenv(name+"_CONSUMER_GROUP", group)
	}
}

func TestParseConsumerModeDefaultsToKafka(t *testing.T) {
	for _, raw := range []string{"", "kafka"} {
		got, err := parseConsumerMode("FULFILLMENT_MODE", raw)
		if err != nil || got != modeKafka {
			t.Fatalf("parseConsumerMode(%q) = %q, %v; want kafka", raw, got, err)
		}
	}
}

func TestParseConsumerModeRejectsPermissiveWithTheFleetMessage(t *testing.T) {
	_, err := parseConsumerMode("LAYOUT_MODE", "permissive")
	const want = "LAYOUT_MODE=permissive is not supported: this context runs on real Kafka"
	if err == nil || err.Error() != want {
		t.Fatalf("permissive = %v, want exactly %q", err, want)
	}
}

func TestParseConsumerModeRejectsAnUnknownValueNamingTheVariable(t *testing.T) {
	_, err := parseConsumerMode("FULFILLMENT_MODE", "Kafka")
	if err == nil || !strings.Contains(err.Error(), "FULFILLMENT_MODE") {
		t.Fatalf("an unknown mode must be a boot error naming the variable, got %v", err)
	}
}

// The env var names are part of the deployment contract (the chart and
// .claude/rules/integration-events.md): pin them.
func TestConsumerEnvsPinTheNames(t *testing.T) {
	var got []string
	for _, e := range consumerEnvs {
		got = append(got, e.modeEnv+":"+e.groupEnv)
	}
	want := "FULFILLMENT_MODE:FULFILLMENT_CONSUMER_GROUP,LAYOUT_MODE:LAYOUT_CONSUMER_GROUP"
	if strings.Join(got, ",") != want {
		t.Fatalf("envs = %v, want %s", got, want)
	}
}

func TestEveryConsumerEnvHasABuilder(t *testing.T) {
	builders := consumerBuilders(adapters{})
	for _, e := range consumerEnvs {
		if builders[e.modeEnv] == nil {
			t.Errorf("no builder for %s", e.modeEnv)
		}
	}
	if len(builders) != len(consumerEnvs) {
		t.Errorf("%d builders for %d consumers", len(builders), len(consumerEnvs))
	}
}

func TestPlanConsumersPlansBothWithoutAnyMode(t *testing.T) {
	setConsumerEnv(t, "", "")
	t.Setenv("FULFILLMENT_CONSUMER_GROUP", "ful-g")
	t.Setenv("LAYOUT_CONSUMER_GROUP", "lay-g")
	planned, err := planConsumers()
	if err != nil || len(planned) != 2 {
		t.Fatalf("planned = %d, %v; want the two consumers, always on", len(planned), err)
	}
	var groups []string
	for _, p := range planned {
		groups = append(groups, p.group)
	}
	if strings.Join(groups, ",") != "ful-g,lay-g" {
		t.Fatalf("groups = %v", groups)
	}
}

func TestPlanConsumersNeedsEveryGroup(t *testing.T) {
	for _, name := range []string{"FULFILLMENT", "LAYOUT"} {
		setConsumerEnv(t, "", "g")
		t.Setenv(name+"_CONSUMER_GROUP", "")
		_, err := planConsumers()
		if err == nil || !strings.Contains(err.Error(), name+"_CONSUMER_GROUP is required") {
			t.Fatalf("%s group missing = %v, want a boot error naming %s_CONSUMER_GROUP", name, err, name)
		}
	}
}

func TestPlanConsumersRejectsPermissiveAndUnknownModes(t *testing.T) {
	setConsumerEnv(t, "", "g")
	t.Setenv("LAYOUT_MODE", "permissive")
	if _, err := planConsumers(); err == nil || !strings.Contains(err.Error(), "LAYOUT_MODE=permissive is not supported") {
		t.Fatalf("permissive = %v", err)
	}
	t.Setenv("LAYOUT_MODE", "strict")
	if _, err := planConsumers(); err == nil || !strings.Contains(err.Error(), "unknown LAYOUT_MODE") {
		t.Fatalf("unknown mode = %v", err)
	}
}

func TestPlanConsumersReportsEveryProblemAtOnce(t *testing.T) {
	setConsumerEnv(t, "", "")
	t.Setenv("FULFILLMENT_MODE", "permissive")
	_, err := planConsumers()
	if err == nil {
		t.Fatal("misconfiguration must be a boot error")
	}
	for _, want := range []string{"FULFILLMENT_MODE=permissive", "LAYOUT_CONSUMER_GROUP"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
}

// Every consumer runs in its own goroutine and stops, closing its reader and
// dead-letter writer, when its worker is stopped. The broker is unreachable:
// the readers dial lazily, so boot never blocks on Kafka.
func TestStartConsumersRunsAndStopsEveryConsumer(t *testing.T) {
	setConsumerEnv(t, "", "")
	t.Setenv("FULFILLMENT_CONSUMER_GROUP", "ful-g")
	t.Setenv("LAYOUT_CONSUMER_GROUP", "lay-g")
	planned, err := planConsumers()
	if err != nil {
		t.Fatal(err)
	}
	logger, logs := quietLogger()

	workers := startConsumers(adapters{}, []string{"127.0.0.1:1"}, planned, logger)
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
	for _, want := range []string{"fulfillment consumer", "layout consumer"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs lack %q:\n%s", want, logs.String())
		}
	}
	if strings.Contains(logs.String(), "consumer stopped") || strings.Contains(logs.String(), "close failed") {
		t.Fatalf("a cancelled consumer must stop quietly:\n%s", logs.String())
	}
}
