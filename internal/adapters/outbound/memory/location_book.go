package memory

import (
	"context"
	"maps"
	"sync"

	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/domain/travel"
)

type locationRow struct {
	zoneID         string
	decommissioned bool
}

// LocationBook is the in-memory ports.LocationBook.
type LocationBook struct {
	mu        sync.Mutex
	zones     map[string]repository.Zone
	locations map[string]locationRow
	points    map[string]travel.Point
}

// NewLocationBook constructs an empty LocationBook.
func NewLocationBook() *LocationBook {
	return &LocationBook{
		zones:     map[string]repository.Zone{},
		locations: map[string]locationRow{},
		points:    map[string]travel.Point{},
	}
}

// Places implements ports.LocationBook.
func (b *LocationBook) Places(_ context.Context, codes []string) (map[string]repository.Place, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make(map[string]repository.Place, len(codes))
	for _, code := range codes {
		loc, hasLoc := b.locations[code]
		pt, hasPoint := b.points[code]
		if !hasLoc && !hasPoint {
			continue
		}
		p := repository.Place{Code: code, SiteCode: b.zones[loc.zoneID].SiteCode, Decommissioned: loc.decommissioned}
		if hasPoint {
			p.Point = &travel.Point{X: pt.X, Y: pt.Y, Z: pt.Z}
		}
		out[code] = p
	}
	return out, nil
}

// SaveZone implements ports.LocationBook.
func (b *LocationBook) SaveZone(_ context.Context, z repository.Zone) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.zones[z.ID] = z
	return nil
}

// SaveLocation implements ports.LocationBook.
func (b *LocationBook) SaveLocation(_ context.Context, code, zoneID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.locations[code] = locationRow{zoneID: zoneID, decommissioned: b.locations[code].decommissioned}
	return nil
}

// DecommissionLocation implements ports.LocationBook.
func (b *LocationBook) DecommissionLocation(_ context.Context, code string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.locations[code] = locationRow{zoneID: b.locations[code].zoneID, decommissioned: true}
	return nil
}

// SaveGeometry implements ports.LocationBook.
func (b *LocationBook) SaveGeometry(_ context.Context, code string, x, y, z float64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.points[code] = travel.Point{X: x, Y: y, Z: z}
	return nil
}

// Snapshot implements Snapshotter.
func (b *LocationBook) Snapshot() func() {
	b.mu.Lock()
	defer b.mu.Unlock()
	zones, locations, points := maps.Clone(b.zones), maps.Clone(b.locations), maps.Clone(b.points)
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.zones, b.locations, b.points = zones, locations, points
	}
}
