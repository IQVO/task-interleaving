package usecases

import (
	"context"
	"errors"
	"fmt"

	"github.com/claudioed/task-interleaving/internal/application/ports"
	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/domain/policy"
	"github.com/claudioed/task-interleaving/internal/domain/recommend"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
	"github.com/claudioed/task-interleaving/internal/domain/travel"
)

// Ranker runs the pure recommender over the local copies: the AVAILABLE tasks
// of the site and the geometry of their first locations. It reads only.
type Ranker struct {
	Pool ports.TaskPool
	Book ports.LocationBook
}

// Origin resolves the place a station stands at: the current geometry of the
// location when the layout copy has it, else the recorded point. It also
// returns the site of the location's zone ("" while unknown). A decommissioned
// location keeps its geometry here: the operator stood there.
func (r Ranker) Origin(ctx context.Context, location string, recorded *travel.Point) (travel.Place, string, error) {
	places, err := r.Book.Places(ctx, []string{location})
	if err != nil {
		return travel.Place{}, "", fmt.Errorf("read location: %w", err)
	}
	place := travel.Place{Location: location, Point: recorded}
	book, ok := places[location]
	if !ok {
		return place, "", nil
	}
	if book.Point != nil {
		place.Point = book.Point
	}
	return place, book.SiteCode, nil
}

// Rank returns the ranked candidates for a station standing at origin under
// the site's policy (recommend.Recommend); completed is the task just done,
// never recommended. recommend.ErrOriginUnknown comes back as is.
func (r Ranker) Rank(ctx context.Context, pol policy.Policy, origin travel.Place, completed recommend.TaskID) ([]recommend.Candidate, error) {
	tasks, err := r.Pool.Available(ctx, string(pol.SiteID()))
	if err != nil {
		return nil, fmt.Errorf("read task pool: %w", err)
	}
	codes := make([]string, 0, len(tasks))
	for _, t := range tasks {
		codes = append(codes, t.FromLocation)
	}
	places, err := r.Book.Places(ctx, codes)
	if err != nil {
		return nil, fmt.Errorf("read locations: %w", err)
	}
	pool := make([]recommend.OpenTask, 0, len(tasks))
	for _, t := range tasks {
		pool = append(pool, openTask(t, places))
	}
	return recommend.Recommend(recommend.Input{
		Site:      pol.SiteID(),
		Origin:    origin,
		Pool:      pool,
		Policy:    pol,
		Completed: completed,
		Model:     recommend.RectilinearFor(pol),
	})
}

// openTask turns a pool row into the recommender's task. A task whose first
// location is unseen, has no geometry or is decommissioned has an unknown
// point, so the recommender skips it.
func openTask(t repository.Task, places map[string]repository.Place) recommend.OpenTask {
	ot := recommend.OpenTask{
		ID:           recommend.TaskID(t.ID),
		Type:         policy.TaskType(t.Type),
		Site:         policy.SiteID(t.SiteID),
		FromLocation: t.FromLocation,
		ToLocation:   t.ToLocation,
	}
	if p, ok := places[t.FromLocation]; ok && !p.Decommissioned {
		ot.FromPoint = p.Point
	}
	return ot
}

// TripWriter loads and saves StationTrip aggregates and hands the facts they
// raised to the outbox. It must run inside a UnitOfWork.
type TripWriter struct {
	Trips  ports.StationTripRepo
	Events ports.EventSink
}

// load returns the station's trip and the version the save is guarded by; a
// station never seen starts an empty trip at version 0.
func (w TripWriter) load(ctx context.Context, id stationtrip.StationID) (*stationtrip.StationTrip, int64, error) {
	trip, err := w.Trips.Get(ctx, id)
	if errors.Is(err, repository.ErrStationNotFound) {
		fresh, err := stationtrip.New(id)
		return fresh, 0, err
	}
	if err != nil {
		return nil, 0, fmt.Errorf("load station: %w", err)
	}
	return trip, trip.Version(), nil
}

// persist saves the trip guarded by the loaded version and enqueues its facts.
func (w TripWriter) persist(ctx context.Context, trip *stationtrip.StationTrip, loaded int64) error {
	if err := w.Trips.Save(ctx, trip, loaded); err != nil {
		return err
	}
	if err := w.Events.PublishTripEvents(ctx, trip.DomainEvents()...); err != nil {
		return fmt.Errorf("enqueue events: %w", err)
	}
	trip.ClearDomainEvents()
	return nil
}
