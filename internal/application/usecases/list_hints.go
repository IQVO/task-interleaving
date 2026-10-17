package usecases

import (
	"context"
	"fmt"

	"github.com/claudioed/task-interleaving/internal/application/ports"
	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/domain/policy"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
)

// ListHintsQuery is the input of ListHints. Empty filters mean "no filter";
// Limit 0 means DefaultListLimit.
type ListHintsQuery struct {
	StationID string
	SiteID    string
	State     string
	Cursor    string
	Limit     int
}

// HintPage is one page of hints, newest first. NextCursor is empty on the last
// page.
type HintPage struct {
	Items      []repository.HintRecord
	NextCursor string
}

// ListHints lists the hint history.
type ListHints struct {
	Trips ports.StationTripRepo
}

// Handle runs the use case. Invalid filters, limit or cursor are the domain's
// ErrInvalid* errors, ErrInvalidLimit, ErrInvalidState and ErrInvalidCursor.
func (uc *ListHints) Handle(ctx context.Context, q ListHintsQuery) (HintPage, error) {
	filter, err := hintFilter(q)
	if err != nil {
		return HintPage{}, err
	}
	limit, err := listLimit(q.Limit)
	if err != nil {
		return HintPage{}, err
	}
	after, err := DecodeCursor(q.Cursor)
	if err != nil {
		return HintPage{}, err
	}
	recs, err := uc.Trips.ListHints(ctx, filter, stationtrip.HintID(after), limit+1)
	if err != nil {
		return HintPage{}, fmt.Errorf("list hints: %w", err)
	}
	if len(recs) <= limit {
		return HintPage{Items: recs}, nil
	}
	recs = recs[:limit]
	return HintPage{Items: recs, NextCursor: EncodeCursor(string(recs[limit-1].Hint.ID))}, nil
}

func hintFilter(q ListHintsQuery) (repository.HintFilter, error) {
	var f repository.HintFilter
	if q.StationID != "" {
		id, err := stationtrip.NewStationID(q.StationID)
		if err != nil {
			return f, err
		}
		f.StationID = id
	}
	if q.SiteID != "" {
		id, err := policy.NewSiteID(q.SiteID)
		if err != nil {
			return f, err
		}
		f.SiteID = id
	}
	if q.State != "" {
		state := stationtrip.HintState(q.State)
		if !state.Valid() {
			return f, ErrInvalidState
		}
		f.State = state
	}
	return f, nil
}
