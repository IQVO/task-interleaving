package main

import (
	"context"
	"log/slog"
	"time"

	inboundhttp "github.com/claudioed/task-interleaving/internal/adapters/inbound/http"
	"github.com/claudioed/task-interleaving/internal/application/ports"
)

// The Idempotency-Key retention (ADR 0013, Amendment 2): a stored answer
// replays for inboundhttp.IdempotencyWindow only, but nothing deleted the
// rows the window had already expired, so the table grew with traffic. The
// purge worker closes that gap: at once on start and then every
// idempotencyPurgeInterval it deletes the rows older than the window, at
// most idempotencyPurgeBatch per statement (the store picks them with FOR
// UPDATE SKIP LOCKED, so one pass never holds a long lock, waits on a
// request, or deletes a row a request holds). The two values are fixed on
// purpose, like the retention of the sibling services: the age is the replay
// window itself, the rest is housekeeping, not configuration.
const (
	idempotencyPurgeInterval = time.Hour
	idempotencyPurgeBatch    = 500
)

// purgeIdempotencyKeys runs batches until one comes back short (the backlog
// is gone) or ctx ends, and returns how many rows it deleted.
func purgeIdempotencyKeys(ctx context.Context, store ports.IdempotencyStore, clock ports.Clock, batch int) (int, error) {
	total := 0
	for ctx.Err() == nil {
		n, err := store.PurgeExpired(ctx, clock.Now().UTC().Add(-inboundhttp.IdempotencyWindow), batch)
		total += n
		if err != nil || n < batch {
			return total, err
		}
	}
	return total, nil
}

// startIdempotencyPurge starts the always-on retention worker: one pass at
// once, then one every interval. A failed pass is logged and retried at the
// next tick: the purge is idempotent, and a row that outlives its window is
// never replayed (Lookup checks the age), so a failure costs only disk.
func startIdempotencyPurge(store ports.IdempotencyStore, clock ports.Clock, logger *slog.Logger) *worker {
	return launch("idempotency-purge", func(ctx context.Context) {
		logger.Info("idempotency purge running", "interval", idempotencyPurgeInterval,
			"ttl", inboundhttp.IdempotencyWindow, "batch", idempotencyPurgeBatch)
		ticker := time.NewTicker(idempotencyPurgeInterval)
		defer ticker.Stop()
		for {
			switch n, err := purgeIdempotencyKeys(ctx, store, clock, idempotencyPurgeBatch); {
			case err != nil && ctx.Err() == nil:
				logger.Error("idempotency purge failed", "deleted", n, "error", err)
			case n > 0:
				logger.Info("idempotency purge done", "deleted", n)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})
}
