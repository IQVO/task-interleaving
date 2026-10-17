package usecases

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/claudioed/task-interleaving/internal/application/ports"
	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
)

// expireBatch bounds how many stations one sweep expires.
const expireBatch = 200

// ExpireHints is the sweeper: every Active hint at or past its expires_at
// becomes Expired and the fact is enqueued. Each station is expired in its own
// unit of work.
type ExpireHints struct {
	TripWriter
	UoW   ports.UnitOfWork
	Clock ports.Clock
}

// Handle runs one sweep and returns how many hints expired. A station whose
// write loses a race is skipped (the next sweep sees it again); other failures
// do not stop the sweep and are returned joined.
func (uc *ExpireHints) Handle(ctx context.Context) (int, error) {
	now := uc.Clock.Now().UTC()
	ids, err := uc.Trips.ExpiredActive(ctx, now, expireBatch)
	if err != nil {
		return 0, fmt.Errorf("find expired hints: %w", err)
	}
	expired := 0
	var failures []error
	for _, id := range ids {
		done, err := uc.expire(ctx, id, now)
		if errors.Is(err, repository.ErrConcurrentModification) {
			continue
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("expire hint of %s: %w", id, err))
			continue
		}
		if done {
			expired++
		}
	}
	return expired, errors.Join(failures...)
}

func (uc *ExpireHints) expire(ctx context.Context, id stationtrip.StationID, now time.Time) (bool, error) {
	done := false
	err := uc.UoW.Do(ctx, func(ctx context.Context) error {
		trip, err := uc.Trips.Get(ctx, id)
		if errors.Is(err, repository.ErrStationNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("load station: %w", err)
		}
		loaded := trip.Version()
		if !trip.Expire(now) {
			return nil
		}
		done = true
		return uc.persist(ctx, trip, loaded)
	})
	if err != nil {
		return false, err
	}
	return done, nil
}
