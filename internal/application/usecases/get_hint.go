package usecases

import (
	"context"

	"github.com/claudioed/task-interleaving/internal/application/ports"
	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
)

// GetHint reads one hint of the history.
type GetHint struct {
	Trips ports.StationTripRepo
}

// Handle returns the hint, repository.ErrHintNotFound for an unknown id, or
// stationtrip.ErrInvalidHintID.
func (uc *GetHint) Handle(ctx context.Context, hintID string) (repository.HintRecord, error) {
	id, err := stationtrip.NewHintID(hintID)
	if err != nil {
		return repository.HintRecord{}, err
	}
	return uc.Trips.GetHint(ctx, id)
}
