// Package expiry is the hint-expiry worker: a timer-driven driving adapter
// that calls the ExpireHints use case on an interval, so an Active hint whose
// expires_at has passed becomes Expired (and its analytics fact is enqueued)
// even when no TaskClaimed or TaskCompleted arrives for that station.
//
// It owns no business rule. The use case decides what expires; the worker only
// decides WHEN to ask. A failing sweep is logged and retried after the
// interval -- the hints stay Active, so nothing is lost -- and the worker never
// dials anything itself.
package expiry

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// DefaultInterval is the sweep interval a composition root uses when
// HINT_EXPIRY_INTERVAL is unset. A hint lives for seconds to minutes (the
// policy's hint_ttl), so 5s bounds how late an expiry can be observed.
const DefaultInterval = 5 * time.Second

// Sweeper is the ExpireHints use case as the worker sees it: one sweep, how
// many hints expired, and the (possibly joined) failures of the stations that
// could not be expired.
type Sweeper interface {
	Handle(ctx context.Context) (int, error)
}

// Timer is the worker's source of waiting: After returns a channel that
// delivers once d has elapsed. SystemTimer is the production implementation;
// tests substitute one they fire by hand.
type Timer interface {
	After(d time.Duration) <-chan time.Time
}

// SystemTimer waits with the runtime clock.
type SystemTimer struct{}

// After implements Timer.
func (SystemTimer) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Worker sweeps expired hints every interval.
type Worker struct {
	sweeper  Sweeper
	logger   *slog.Logger
	timer    Timer
	interval time.Duration
}

// Option customises a Worker.
type Option func(*Worker)

// WithInterval sets the pause between sweeps. Non-positive values are ignored.
func WithInterval(d time.Duration) Option {
	return func(w *Worker) {
		if d > 0 {
			w.interval = d
		}
	}
}

// WithTimer replaces the runtime timer (a fake clock in tests). A nil timer is
// ignored.
func WithTimer(t Timer) Option {
	return func(w *Worker) {
		if t != nil {
			w.timer = t
		}
	}
}

// NewWorker constructs a worker driving sweeper.
func NewWorker(sweeper Sweeper, logger *slog.Logger, opts ...Option) *Worker {
	if logger == nil {
		logger = slog.Default()
	}
	w := &Worker{sweeper: sweeper, logger: logger, timer: SystemTimer{}, interval: DefaultInterval}
	for _, o := range opts {
		o(w)
	}
	return w
}

// SweepOnce runs one sweep and returns how many hints it expired. A sweep that
// expired some stations and failed on others reports both: the count and the
// error.
func (w *Worker) SweepOnce(ctx context.Context) (int, error) {
	return w.sweeper.Handle(ctx)
}

// Run sweeps until ctx is cancelled and then returns ctx.Err(). The first sweep
// runs at once, then one per interval. A failing sweep is logged at ERROR (a
// cancelled context is not a failure) and the loop carries on.
func (w *Worker) Run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := w.SweepOnce(ctx)
		w.report(ctx, n, err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-w.timer.After(w.interval):
		}
	}
}

func (w *Worker) report(ctx context.Context, expired int, err error) {
	if err != nil && !errors.Is(err, context.Canceled) {
		w.logger.ErrorContext(ctx, "hint expiry sweep failed", "expired", expired, "error", err)
		return
	}
	if expired > 0 {
		w.logger.InfoContext(ctx, "hints expired", "expired", expired)
	}
}
