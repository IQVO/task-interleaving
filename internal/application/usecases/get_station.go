package usecases

import (
	"context"

	"github.com/claudioed/task-interleaving/internal/application/ports"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
)

// GetStation reads what the context knows about a station.
type GetStation struct {
	Trips ports.StationTripRepo
}

// Handle returns the station's trip, repository.ErrStationNotFound when no
// completion was recorded for it, or stationtrip.ErrInvalidStationID.
func (uc *GetStation) Handle(ctx context.Context, stationID string) (*stationtrip.StationTrip, error) {
	id, err := stationtrip.NewStationID(stationID)
	if err != nil {
		return nil, err
	}
	return uc.Trips.Get(ctx, id)
}
