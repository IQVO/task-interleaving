package expiry_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/task-interleaving/internal/adapters/inbound/expiry"
)

const wait = 5 * time.Second

// result is one scripted answer of the fake sweeper.
type result struct {
	n   int
	err error
}

// fakeSweeper answers from a script (the last answer repeats) and announces
// every call on calls, so a test can step the worker deterministically.
type fakeSweeper struct {
	script []result
	calls  chan int
	n      int
}

func newSweeper(script ...result) *fakeSweeper {
	return &fakeSweeper{script: script, calls: make(chan int, 100)}
}

func (s *fakeSweeper) Handle(context.Context) (int, error) {
	r := s.script[min(s.n, len(s.script)-1)]
	s.n++
	s.calls <- s.n
	return r.n, r.err
}

// fakeTimer is the fake clock: After records the requested pause and hands out
// a channel the test fires by hand, so no test sleeps in real time.
type fakeTimer struct {
	waits chan time.Duration
	fire  chan time.Time
}

func newTimer() *fakeTimer {
	return &fakeTimer{waits: make(chan time.Duration, 100), fire: make(chan time.Time)}
}

func (f *fakeTimer) After(d time.Duration) <-chan time.Time {
	f.waits <- d
	return f.fire
}

func awaitCall(t *testing.T, s *fakeSweeper, want int) {
	t.Helper()
	select {
	case got := <-s.calls:
		if got != want {
			t.Fatalf("sweep #%d, want #%d", got, want)
		}
	case <-time.After(wait):
		t.Fatalf("sweep #%d never ran", want)
	}
}

func awaitWait(t *testing.T, f *fakeTimer) time.Duration {
	t.Helper()
	select {
	case d := <-f.waits:
		return d
	case <-time.After(wait):
		t.Fatal("the worker never waited for the next sweep")
		return 0
	}
}

func tick(t *testing.T, f *fakeTimer) {
	t.Helper()
	select {
	case f.fire <- time.Time{}:
	case <-time.After(wait):
		t.Fatal("the worker was not waiting for the timer")
	}
}

// started is a running worker.
type started struct {
	cancel context.CancelFunc
	done   chan error
}

func start(w *expiry.Worker) started {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	return started{cancel: cancel, done: done}
}

// stop cancels the worker and returns what Run returned.
func (s started) stop(t *testing.T) error {
	t.Helper()
	s.cancel()
	select {
	case err := <-s.done:
		return err
	case <-time.After(wait):
		t.Fatal("Run did not return after the context was cancelled")
		return nil
	}
}

func logger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, nil))
}

func TestRun_SweepsAtOnceThenOncePerTick(t *testing.T) {
	sw, timer := newSweeper(result{n: 2}, result{n: 0}, result{n: 1}), newTimer()
	run := start(expiry.NewWorker(sw, logger(&bytes.Buffer{}), expiry.WithTimer(timer)))

	awaitCall(t, sw, 1)
	awaitWait(t, timer)
	tick(t, timer)
	awaitCall(t, sw, 2)
	awaitWait(t, timer)
	tick(t, timer)
	awaitCall(t, sw, 3)
	awaitWait(t, timer)

	if err := run.stop(t); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
	if len(sw.calls) != 0 {
		t.Fatalf("%d extra sweeps ran without a tick", len(sw.calls))
	}
}

func TestRun_WaitsTheDefaultIntervalOfFiveSeconds(t *testing.T) {
	sw, timer := newSweeper(result{}), newTimer()
	run := start(expiry.NewWorker(sw, nil, expiry.WithTimer(timer)))
	awaitCall(t, sw, 1)
	if d := awaitWait(t, timer); d != 5*time.Second {
		t.Fatalf("waited %v, want the default 5s", d)
	}
	_ = run.stop(t)
}

func TestRun_WaitsTheConfiguredInterval(t *testing.T) {
	sw, timer := newSweeper(result{}), newTimer()
	run := start(expiry.NewWorker(sw, nil, expiry.WithTimer(timer), expiry.WithInterval(2*time.Second)))
	awaitCall(t, sw, 1)
	if d := awaitWait(t, timer); d != 2*time.Second {
		t.Fatalf("waited %v, want 2s", d)
	}
	_ = run.stop(t)
}

func TestOptions_IgnoreNonPositiveIntervalAndNilTimer(t *testing.T) {
	sw, timer := newSweeper(result{}), newTimer()
	run := start(expiry.NewWorker(sw, nil, expiry.WithTimer(timer), expiry.WithInterval(0), expiry.WithInterval(-time.Second), expiry.WithTimer(nil)))
	awaitCall(t, sw, 1)
	if d := awaitWait(t, timer); d != expiry.DefaultInterval {
		t.Fatalf("waited %v, want the default after non-positive intervals", d)
	}
	_ = run.stop(t)
}

func TestRun_UsesTheSystemTimerByDefault(t *testing.T) {
	sw := newSweeper(result{})
	run := start(expiry.NewWorker(sw, nil, expiry.WithInterval(time.Millisecond)))
	awaitCall(t, sw, 1)
	awaitCall(t, sw, 2)
	_ = run.stop(t)
}

func TestSystemTimer_DeliversAfterThePause(t *testing.T) {
	select {
	case <-expiry.SystemTimer{}.After(time.Millisecond):
	case <-time.After(wait):
		t.Fatal("the system timer never fired")
	}
}

// Fault injection: a sweep that fails -- wholly or for some stations only -- is
// logged with its error and the number it did expire, and the next tick sweeps
// again.
func TestRun_FailedSweepIsLoggedAndTheLoopCarriesOn(t *testing.T) {
	boom := errors.New("expire hint of STN-07: database is down")
	sw, timer, buf := newSweeper(result{n: 1, err: boom}, result{n: 3}), newTimer(), &bytes.Buffer{}
	run := start(expiry.NewWorker(sw, logger(buf), expiry.WithTimer(timer)))

	awaitCall(t, sw, 1)
	awaitWait(t, timer)
	tick(t, timer)
	awaitCall(t, sw, 2)
	awaitWait(t, timer)
	if err := run.stop(t); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}

	log := buf.String()
	for _, want := range []string{"level=ERROR", "hint expiry sweep failed", "expired=1", "database is down", "level=INFO", "hints expired", "expired=3"} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
}

func TestRun_ASweepThatExpiredNothingIsSilent(t *testing.T) {
	sw, timer, buf := newSweeper(result{}), newTimer(), &bytes.Buffer{}
	run := start(expiry.NewWorker(sw, logger(buf), expiry.WithTimer(timer)))
	awaitCall(t, sw, 1)
	awaitWait(t, timer)
	_ = run.stop(t)
	if buf.Len() != 0 {
		t.Fatalf("an empty sweep logged:\n%s", buf)
	}
}

func TestRun_ACancelledSweepIsNotLoggedAsAFailure(t *testing.T) {
	sw, timer, buf := newSweeper(result{err: context.Canceled}), newTimer(), &bytes.Buffer{}
	run := start(expiry.NewWorker(sw, logger(buf), expiry.WithTimer(timer)))
	awaitCall(t, sw, 1)
	awaitWait(t, timer)
	_ = run.stop(t)
	if buf.Len() != 0 {
		t.Fatalf("a cancelled sweep logged:\n%s", buf)
	}
}

func TestRun_ReturnsImmediatelyForAnAlreadyCancelledContext(t *testing.T) {
	sw := newSweeper(result{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := expiry.NewWorker(sw, nil, expiry.WithTimer(newTimer())).Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
	if sw.n != 0 {
		t.Fatalf("%d sweeps ran on a cancelled context", sw.n)
	}
}

func TestSweepOnce_ReturnsTheUseCaseAnswerUnchanged(t *testing.T) {
	boom := errors.New("boom")
	n, err := expiry.NewWorker(newSweeper(result{n: 4, err: boom}), nil).SweepOnce(context.Background())
	if n != 4 || !errors.Is(err, boom) {
		t.Fatalf("SweepOnce = %d, %v; want 4, boom", n, err)
	}
}
