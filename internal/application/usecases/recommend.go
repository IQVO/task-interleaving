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

// RecommendInput is the input of Recommend. FromLocation "" means the
// station's recorded position.
type RecommendInput struct {
	StationID    string
	FromLocation string
}

// Recommendation is the simulation's answer. Candidates is empty when no hint
// would be issued.
type Recommendation struct {
	StationID      stationtrip.StationID
	SiteID         policy.SiteID
	OriginLocation string
	PolicyVersion  int64
	Candidates     []recommend.Candidate
}

// Recommend is the read-only simulation: it runs the same recommender that
// issues hints against the current pool and the site's policy, and issues
// nothing, publishes nothing and changes nothing.
type Recommend struct {
	Trips    ports.StationTripRepo
	Policies ports.PolicyRepo
	Ranker   Ranker
}

// Handle runs the use case. The site is the station's site, else the site of
// FromLocation. Errors: stationtrip.ErrInvalidStationID,
// ErrStationPositionUnknown (no FromLocation and no recorded position),
// recommend.ErrOriginUnknown (no site or no geometry for the origin),
// ErrPolicyNotDeclared.
func (uc *Recommend) Handle(ctx context.Context, in RecommendInput) (Recommendation, error) {
	station, err := stationtrip.NewStationID(in.StationID)
	if err != nil {
		return Recommendation{}, err
	}
	trip, err := uc.Trips.Get(ctx, station)
	if err != nil && !errors.Is(err, repository.ErrStationNotFound) {
		return Recommendation{}, fmt.Errorf("load station: %w", err)
	}
	origin, err := uc.origin(ctx, trip, in.FromLocation)
	if err != nil {
		return Recommendation{}, err
	}
	pol, err := uc.Policies.Get(ctx, origin.site)
	if errors.Is(err, repository.ErrPolicyNotFound) {
		return Recommendation{}, ErrPolicyNotDeclared
	}
	if err != nil {
		return Recommendation{}, fmt.Errorf("load policy: %w", err)
	}
	cands, err := uc.Ranker.Rank(ctx, pol.Policy, origin.place, origin.completed)
	if errors.Is(err, recommend.ErrOriginUnknown) {
		return Recommendation{}, fmt.Errorf("%w: %s", err, origin.place.Location)
	}
	if err != nil {
		return Recommendation{}, err
	}
	return Recommendation{
		StationID:      station,
		SiteID:         origin.site,
		OriginLocation: origin.place.Location,
		PolicyVersion:  pol.Policy.Version(),
		Candidates:     cands,
	}, nil
}

// simOrigin is where the simulation starts from.
type simOrigin struct {
	site      policy.SiteID
	place     travel.Place
	completed recommend.TaskID
}

// origin resolves the simulation's start: FromLocation, else the recorded
// position; the site is the station's, else the one of the location's zone.
func (uc *Recommend) origin(ctx context.Context, trip *stationtrip.StationTrip, from string) (simOrigin, error) {
	var pos stationtrip.Position
	var hasPos bool
	var completed recommend.TaskID
	if trip != nil {
		pos, hasPos = trip.Position()
		completed, _ = trip.LastCompleted()
	}
	location, recorded := from, (*travel.Point)(nil)
	if location == "" {
		if !hasPos {
			return simOrigin{}, ErrStationPositionUnknown
		}
		location, recorded = pos.Place.Location, pos.Place.Point
	}
	place, bookSite, err := uc.Ranker.Origin(ctx, location, recorded)
	if err != nil {
		return simOrigin{}, err
	}
	site := policy.SiteID(bookSite)
	if hasPos {
		site = pos.Site
	}
	if site == "" {
		return simOrigin{}, fmt.Errorf("%w: %s", recommend.ErrOriginUnknown, location)
	}
	return simOrigin{site: site, place: place, completed: completed}, nil
}
