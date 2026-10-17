package usecases

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/claudioed/task-interleaving/internal/application/ports"
	"github.com/claudioed/task-interleaving/internal/application/repository"
)

// ZoneChange is a decoded ZoneRegistered payload, reduced to what this service
// needs: the zone id, its site and its zone code.
type ZoneChange struct {
	ZoneID   string
	SiteCode string
	ZoneCode string
}

// ApplyZone upserts a zone into the layout copy.
type ApplyZone struct {
	Intake
	Book ports.LocationBook
}

// Handle runs the use case.
func (uc *ApplyZone) Handle(ctx context.Context, eventID string, c ZoneChange) (Outcome, error) {
	if blank(c.ZoneID) || blank(c.SiteCode) {
		return "", invalid(errors.New("zoneId and siteCode are required"))
	}
	z := repository.Zone{ID: c.ZoneID, SiteCode: c.SiteCode, ZoneCode: c.ZoneCode}
	return uc.apply(ctx, LayoutConsumer, eventID, func(ctx context.Context) (Outcome, error) {
		if err := uc.Book.SaveZone(ctx, z); err != nil {
			return "", fmt.Errorf("save zone: %w", err)
		}
		return OutcomeApplied, nil
	})
}

// LocationChange is a decoded LocationSlotRegistered payload, reduced to what
// this service needs: the location code and its zone.
type LocationChange struct {
	Code   string
	ZoneID string
}

// ApplyLocationRegistered upserts a location into the layout copy.
type ApplyLocationRegistered struct {
	Intake
	Book ports.LocationBook
}

// Handle runs the use case.
func (uc *ApplyLocationRegistered) Handle(ctx context.Context, eventID string, c LocationChange) (Outcome, error) {
	if blank(c.Code) || blank(c.ZoneID) {
		return "", invalid(errors.New("locationCode and zoneId are required"))
	}
	return uc.apply(ctx, LayoutConsumer, eventID, func(ctx context.Context) (Outcome, error) {
		if err := uc.Book.SaveLocation(ctx, c.Code, c.ZoneID); err != nil {
			return "", fmt.Errorf("save location: %w", err)
		}
		return OutcomeApplied, nil
	})
}

// ApplyLocationDecommissioned retires a location in the layout copy for good.
type ApplyLocationDecommissioned struct {
	Intake
	Book ports.LocationBook
}

// Handle runs the use case.
func (uc *ApplyLocationDecommissioned) Handle(ctx context.Context, eventID, code string) (Outcome, error) {
	if blank(code) {
		return "", invalid(errors.New("locationCode is required"))
	}
	return uc.apply(ctx, LayoutConsumer, eventID, func(ctx context.Context) (Outcome, error) {
		if err := uc.Book.DecommissionLocation(ctx, code); err != nil {
			return "", fmt.Errorf("decommission location: %w", err)
		}
		return OutcomeApplied, nil
	})
}

// GeometryChange is a decoded LocationGeometryUpdated payload, reduced to the
// point (xM, yM, zM) of a location.
type GeometryChange struct {
	Code    string
	X, Y, Z float64
}

// ApplyGeometryUpdated upserts the point of a location into the layout copy,
// whether or not the location was registered yet.
type ApplyGeometryUpdated struct {
	Intake
	Book ports.LocationBook
}

// Handle runs the use case.
func (uc *ApplyGeometryUpdated) Handle(ctx context.Context, eventID string, c GeometryChange) (Outcome, error) {
	if blank(c.Code) {
		return "", invalid(errors.New("locationCode is required"))
	}
	if !finite(c.X) || !finite(c.Y) || !finite(c.Z) {
		return "", invalid(errors.New("xM, yM and zM must be finite numbers"))
	}
	return uc.apply(ctx, LayoutConsumer, eventID, func(ctx context.Context) (Outcome, error) {
		if err := uc.Book.SaveGeometry(ctx, c.Code, c.X, c.Y, c.Z); err != nil {
			return "", fmt.Errorf("save geometry: %w", err)
		}
		return OutcomeApplied, nil
	})
}

func finite(f float64) bool {
	if math.IsNaN(f) {
		return false
	}
	return !math.IsInf(f, 0)
}
