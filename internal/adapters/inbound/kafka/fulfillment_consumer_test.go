package kafka_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	kafkaconsumer "github.com/claudioed/task-interleaving/internal/adapters/inbound/kafka"
	"github.com/claudioed/task-interleaving/internal/application/repository"
)

// pool stocks the pool through the consumer itself: T0 (the task STN-1 is
// about to complete, at L0) and the two open tasks T-1 (at A) and T-2 (at B).
func stockPool(t *testing.T, c *kafkaconsumer.Consumer) {
	t.Helper()
	handle(t, c, availableMsg("e-a0", "T0", "L0"))
	handle(t, c, availableMsg("e-a1", "T-1", "A"))
	handle(t, c, availableMsg("e-a2", "T-2", "B"))
}

func task(t *testing.T, w *world, id string) repository.Task {
	t.Helper()
	got, found, err := w.pool.Get(bg, id)
	if err != nil || !found {
		t.Fatalf("task %s = %+v, found %v, %v", id, got, found, err)
	}
	return got
}

func TestFulfillment_TaskAvailableFeedsThePool(t *testing.T) {
	w := newWorld()
	handle(t, w.fulfillmentConsumer(), availableMsg("e1", "T-1", "A"))

	got := task(t, w, "T-1")
	if got.Type != "PICK" || got.SiteID != "S1" || got.FromLocation != "A" || got.ToLocation != "A" ||
		got.PriorityBand != "P1" || got.State != repository.TaskAvailable || !slices.Equal(got.RequiredCapabilities, []string{"PICK"}) {
		t.Fatalf("task = %+v, want the pinned TaskAvailable payload in the pool", got)
	}
}

// The whole chain: three tasks enter the pool, STN-1 claims T0 and completes it
// with a policy declared, and the open tasks near L0 become a hint.
func TestFulfillment_CompletionWithAPolicyIssuesAHint(t *testing.T) {
	w := newWorld()
	w.seedLayout(t)
	w.seedPolicy(t)
	c := w.fulfillmentConsumer()
	stockPool(t, c)
	handle(t, c, claimedMsg("e-c0", "T0", "STN-1", "2026-10-17T08:59:30Z"))
	handle(t, c, completedMsg("e-done", "T0", "STN-1"))

	if got := w.log.Names(); !slices.Equal(got, []string{"HintIssued"}) {
		t.Fatalf("events = %v, want one HintIssued", got)
	}
	if got := task(t, w, "T0"); got.State != repository.TaskCompleted {
		t.Fatalf("T0 = %+v, want COMPLETED", got)
	}
}

func TestFulfillment_CompletionWithoutAPolicyIsInert(t *testing.T) {
	w := newWorld()
	w.seedLayout(t)
	c := w.fulfillmentConsumer()
	stockPool(t, c)
	handle(t, c, claimedMsg("e-c0", "T0", "STN-1", "2026-10-17T08:59:30Z"))
	handle(t, c, completedMsg("e-done", "T0", "STN-1"))

	if got := w.log.Names(); len(got) != 0 {
		t.Fatalf("events = %v, want none: a site without a policy is inert", got)
	}
	if _, err := w.trips.Get(bg, "STN-1"); err != nil {
		t.Fatalf("the completion must still record the station position: %v", err)
	}
}

// TaskClaimed carries claimed_at: the claim of a candidate consumes the hint.
func TestFulfillment_ClaimingACandidateConsumesTheHint(t *testing.T) {
	w := newWorld()
	w.seedLayout(t)
	w.seedPolicy(t)
	c := w.fulfillmentConsumer()
	stockPool(t, c)
	handle(t, c, claimedMsg("e-c0", "T0", "STN-1", "2026-10-17T08:59:30Z"))
	handle(t, c, completedMsg("e-done", "T0", "STN-1"))
	handle(t, c, claimedMsg("e-c1", "T-1", "STN-1", "2026-10-17T09:00:30Z"))

	want := []string{"HintIssued", "HintConsumed"}
	if got := w.log.Names(); !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if got := task(t, w, "T-1"); got.State != repository.TaskClaimed || got.ClaimedBy != "STN-1" {
		t.Fatalf("T-1 = %+v, want CLAIMED by STN-1", got)
	}
}

// A TaskClaimed without claimed_at takes the CloudEvents time instead.
func TestFulfillment_ClaimWithoutClaimedAtUsesTheEventTime(t *testing.T) {
	w := newWorld()
	w.seedLayout(t)
	w.seedPolicy(t)
	c := w.fulfillmentConsumer()
	stockPool(t, c)
	handle(t, c, claimedMsg("e-c0", "T0", "STN-1", "2026-10-17T08:59:30Z"))
	handle(t, c, completedMsg("e-done", "T0", "STN-1"))
	handle(t, c, claimedMsg("e-c1", "T-2", "STN-1", ""))

	if got := w.log.Names(); !slices.Equal(got, []string{"HintIssued", "HintConsumed"}) {
		t.Fatalf("events = %v: the event time (inside the hint's window) must stand in for claimed_at", got)
	}
}

// ADR 0003: TaskCompleted names its station, so a completion lands even when no
// TaskClaimed was ever seen (producer change P2 is pending).
func TestFulfillment_CompletionNamesItsStationWithoutAnyClaim(t *testing.T) {
	w := newWorld()
	w.seedLayout(t)
	w.seedPolicy(t)
	c := w.fulfillmentConsumer()
	stockPool(t, c)
	handle(t, c, completedMsg("e-done", "T0", "STN-5"))

	if got := w.log.Names(); !slices.Equal(got, []string{"HintIssued"}) {
		t.Fatalf("events = %v, want a hint for STN-5", got)
	}
	if _, err := w.trips.Get(bg, "STN-5"); err != nil {
		t.Fatalf("STN-5 must have the completion: %v", err)
	}
}

func TestFulfillment_MessagesForAnUnknownTaskNeverFail(t *testing.T) {
	w := newWorld()
	c := w.fulfillmentConsumer()
	handle(t, c, claimedMsg("e1", "T-ghost", "STN-1", ""))
	handle(t, c, completedMsg("e2", "T-other-ghost", "STN-1"))
	if strings.Contains(w.logs.String(), "level=ERROR") {
		t.Fatalf("a claim or completion of an unseen task is not an error:\n%s", w.logs.String())
	}
}

func TestFulfillment_DuplicateEventIDIsAppliedOnce(t *testing.T) {
	w := newWorld()
	c := w.fulfillmentConsumer()
	handle(t, c, availableMsg("e1", "T-1", "A"))
	handle(t, c, availableMsg("e1", "T-1", "B")) // same id, another payload: the replay must not apply

	if got := task(t, w, "T-1"); got.FromLocation != "A" {
		t.Fatalf("from_location = %q, want A: the duplicate id must be skipped", got.FromLocation)
	}
	if !strings.Contains(w.logs.String(), "outcome=duplicate") {
		t.Fatalf("the replay must be logged as a duplicate:\n%s", w.logs.String())
	}
}

func TestFulfillment_UnknownTypeIsIgnoredSilently(t *testing.T) {
	w := newWorld()
	released := fulfillmentMsg("e1", "com.warehouse.wes.fulfillment-execution.task.TaskReleased", "T-1", `{"task_id":"T-1"}`)
	handle(t, w.fulfillmentConsumer(), released)

	if w.uow.count() != 0 || strings.Contains(w.logs.String(), "level=WARN") {
		t.Fatalf("an unknown type must be ignored silently (uow calls %d):\n%s", w.uow.count(), w.logs.String())
	}
}

func TestFulfillment_ShortTypeNameIsNotAMatch(t *testing.T) {
	w := newWorld()
	handle(t, w.fulfillmentConsumer(), fulfillmentMsg("e1", "TaskCompleted", "T0", `{"task_id":"T0","station_id":"STN-1"}`))
	if w.uow.count() != 0 {
		t.Fatal("dispatch must use the FULL type, never a short name")
	}
}

func TestFulfillment_LegacyFlatMessageIsSkippedWithWarnAndCommittedPast(t *testing.T) {
	w := newWorld()
	reader := newFakeReader([]byte(legacyFlat), availableMsg("e1", "T-1", "A"))
	if err := runUntilCommits(t, w.fulfillmentConsumer(), reader, 2); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
	logs := w.logs.String()
	if !strings.Contains(logs, "level=WARN") || !strings.Contains(logs, "not a valid CloudEvent") {
		t.Fatalf("the legacy flat message must be skipped with a WARN:\n%s", logs)
	}
	if got := task(t, w, "T-1"); got.State != repository.TaskAvailable {
		t.Fatalf("T-1 = %+v: the message after the legacy one must still be applied", got)
	}
	if want := []string{"fetch:0", "commit:0", "fetch:1", "commit:1"}; !slices.Equal(reader.log(), want) {
		t.Fatalf("reader log = %v, want %v", reader.log(), want)
	}
}

// Anything that is not a valid CloudEvent, or whose data is missing, is skipped
// or dead-lettered; the consumer never panics or blocks the partition.
func TestFulfillment_PayloadsThatCanNeverBeAppliedAreDeadLettered(t *testing.T) {
	w := newWorld()
	noSite := fulfillmentMsg("e1", typeAvailable, "T-1", `{"task_id":"T-1","task_type":"PICK","from_location":"A"}`)
	noFrom := fulfillmentMsg("e2", typeAvailable, "T-2", `{"task_id":"T-2","task_type":"PICK","site_id":"S1"}`)
	badCaps := fulfillmentMsg("e3", typeAvailable, "T-3", `{"task_id":"T-3","required_capabilities":"PICK"}`)
	noStation := fulfillmentMsg("e4", typeClaimed, "T-1", `{"task_id":"T-1","task_type":"PICK"}`)
	badTime := fulfillmentMsg("e5", typeClaimed, "T-1", `{"task_id":"T-1","station_id":"STN-1","claimed_at":"soon"}`)
	noTask := fulfillmentMsg("e6", typeCompleted, "x", `{"station_id":"STN-1"}`)
	notObject := fulfillmentMsg("e7", typeCompleted, "x", `"oops"`)
	reader := newFakeReader(noSite, noFrom, badCaps, noStation, badTime, noTask, notObject, availableMsg("e8", "T-9", "A"))
	dlq := &fakeDLQWriter{}
	c := w.fulfillmentConsumer()
	c.DLQ, c.DeadLetterInvalid = dlq, true

	if err := runUntilCommits(t, c, reader, 8); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	got := dlq.published()
	if len(got) != 7 || got[0].Topic != kafkaconsumer.FulfillmentTopic+".dlq" {
		t.Fatalf("dead-lettered %d messages (%+v), want the seven poison payloads on the .dlq topic", len(got), got)
	}
	if task(t, w, "T-9").State != repository.TaskAvailable {
		t.Fatal("the valid message after the poison ones must be applied")
	}
	if w.uow.count() != 1 {
		t.Fatalf("%d units of work: a payload that can never be applied must not open one", w.uow.count())
	}
}

func TestFulfillment_PoisonPayloadWithoutADLQIsSkippedWithWarn(t *testing.T) {
	w := newWorld()
	handle(t, w.fulfillmentConsumer(), fulfillmentMsg("e1", typeCompleted, "x", `{"station_id":"STN-1"}`))
	if !strings.Contains(w.logs.String(), "skipping an event that can never be applied") {
		t.Fatalf("a poison payload without a DLQ must be skipped with a WARN:\n%s", w.logs.String())
	}
}

func TestFulfillment_TransientFailureRetriesTheSameMessageThenApplies(t *testing.T) {
	w := newWorld()
	w.pool.fail = 2
	reader := newFakeReader(availableMsg("e1", "T-1", "A"))
	dlq := &fakeDLQWriter{}
	c := w.fulfillmentConsumer()
	c.DLQ = dlq

	if err := runUntilCommits(t, c, reader, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	if n := w.pool.saveCalls(); n != 3 {
		t.Fatalf("Save called %d times, want 3 (fail, fail, succeed)", n)
	}
	if got := task(t, w, "T-1"); got.State != repository.TaskAvailable {
		t.Fatalf("T-1 = %+v, want applied: the rolled-back claim must not block the retry", got)
	}
	if len(dlq.published()) != 0 {
		t.Fatal("a message that eventually succeeds must not be dead-lettered")
	}
	if want := []string{"fetch:0", "commit:0"}; !slices.Equal(reader.log(), want) {
		t.Fatalf("reader log = %v, want exactly one commit after the success", reader.log())
	}
}

func TestFulfillment_ExhaustedAttemptsAreDeadLetteredAndTheClaimIsRolledBack(t *testing.T) {
	w := newWorld()
	w.pool.fail = 1 << 20
	reader := newFakeReader(availableMsg("e1", "T-1", "A"))
	dlq := &fakeDLQWriter{}
	c := w.fulfillmentConsumer()
	c.DLQ = dlq

	if err := runUntilCommits(t, c, reader, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	if n := w.pool.saveCalls(); n != 5 {
		t.Fatalf("Save called %d times, want the 5 attempts", n)
	}
	if got := dlq.published(); len(got) != 1 || got[0].Topic != kafkaconsumer.FulfillmentTopic+".dlq" {
		t.Fatalf("dead-lettered = %+v, want one message on the .dlq topic", got)
	}
	w.pool.mu.Lock()
	w.pool.fail = 0
	w.pool.mu.Unlock()
	handle(t, c, availableMsg("e1", "T-1", "A")) // the id was NOT claimed by the failed attempts
	if got := task(t, w, "T-1"); got.State != repository.TaskAvailable {
		t.Fatalf("T-1 = %+v: the rolled-back claim must let a later redelivery apply", got)
	}
}

// A handler that fails because the process is stopping (ctx cancelled
// mid-handle) must never be dead-lettered nor committed: the message is
// redelivered after the restart.
func TestFulfillment_FailureDuringShutdownIsNeitherDeadLetteredNorCommitted(t *testing.T) {
	w := newWorld()
	ctx, cancel := context.WithCancel(context.Background())
	w.pool.fail = 1 << 20
	w.pool.onSave = cancel
	reader := newFakeReader(availableMsg("e1", "T-1", "A"))
	dlq := &fakeDLQWriter{}
	c := w.fulfillmentConsumer()
	c.Reader, c.DLQ = reader, dlq

	if err := c.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
	if len(dlq.published()) != 0 {
		t.Fatal("a shutdown must never dead-letter a good message")
	}
	if reader.commits() != 0 {
		t.Fatal("a shutdown must leave the offset uncommitted")
	}
}

func TestFulfillment_ConstructorBuildsTheConsumerWithoutDialling(t *testing.T) {
	w := newWorld()
	c := kafkaconsumer.NewFulfillmentConsumer([]string{"127.0.0.1:1"}, "group-from-env", w.fulfillmentUseCases(), w.logger())
	assertConstructed(t, c, kafkaconsumer.FulfillmentTopic,
		kafkaconsumer.TypeTaskAvailable, kafkaconsumer.TypeTaskClaimed, kafkaconsumer.TypeTaskCompleted)
}
