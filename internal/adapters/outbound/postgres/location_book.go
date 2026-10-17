package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/domain/travel"
)

// LocationBook is the pgxpool-backed ports.LocationBook: zones, and locations
// with their decommissioned flag and geometry point on one row.
type LocationBook struct {
	pool *pgxpool.Pool
}

// NewLocationBook constructs a LocationBook over pool.
func NewLocationBook(pool *pgxpool.Pool) *LocationBook { return &LocationBook{pool: pool} }

// Places implements ports.LocationBook.
func (b *LocationBook) Places(ctx context.Context, codes []string) (map[string]repository.Place, error) {
	out := make(map[string]repository.Place, len(codes))
	if len(codes) == 0 {
		return out, nil
	}
	rows, err := queryFor(ctx, b.pool).Query(ctx, `
		SELECT l.location_code, coalesce(z.site_code, ''), l.decommissioned, l.x_m, l.y_m, l.z_m
		FROM locations l
		LEFT JOIN zones z ON z.zone_id = l.zone_id
		WHERE l.location_code = ANY($1)
	`, codes)
	if err != nil {
		return nil, fmt.Errorf("read places: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			p       repository.Place
			x, y, z *float64
		)
		if err := rows.Scan(&p.Code, &p.SiteCode, &p.Decommissioned, &x, &y, &z); err != nil {
			return nil, fmt.Errorf("scan place: %w", err)
		}
		if x != nil && y != nil && z != nil {
			p.Point = &travel.Point{X: *x, Y: *y, Z: *z}
		}
		out[p.Code] = p
	}
	return out, rows.Err()
}

// SaveZone implements ports.LocationBook.
func (b *LocationBook) SaveZone(ctx context.Context, z repository.Zone) error {
	_, err := queryFor(ctx, b.pool).Exec(ctx, `
		INSERT INTO zones (zone_id, site_code, zone_code) VALUES ($1, $2, $3)
		ON CONFLICT (zone_id) DO UPDATE
		SET site_code = EXCLUDED.site_code, zone_code = EXCLUDED.zone_code, updated_at = now()
	`, z.ID, z.SiteCode, z.ZoneCode)
	if err != nil {
		return fmt.Errorf("save zone: %w", err)
	}
	return nil
}

// SaveLocation implements ports.LocationBook. An existing row keeps its
// decommissioned flag and its geometry.
func (b *LocationBook) SaveLocation(ctx context.Context, code, zoneID string) error {
	_, err := queryFor(ctx, b.pool).Exec(ctx, `
		INSERT INTO locations (location_code, zone_id) VALUES ($1, $2)
		ON CONFLICT (location_code) DO UPDATE SET zone_id = EXCLUDED.zone_id, updated_at = now()
	`, code, zoneID)
	if err != nil {
		return fmt.Errorf("save location: %w", err)
	}
	return nil
}

// DecommissionLocation implements ports.LocationBook. The row stays, so a late
// registration cannot bring the location back.
func (b *LocationBook) DecommissionLocation(ctx context.Context, code string) error {
	_, err := queryFor(ctx, b.pool).Exec(ctx, `
		INSERT INTO locations (location_code, decommissioned) VALUES ($1, true)
		ON CONFLICT (location_code) DO UPDATE SET decommissioned = true, updated_at = now()
	`, code)
	if err != nil {
		return fmt.Errorf("decommission location: %w", err)
	}
	return nil
}

// SaveGeometry implements ports.LocationBook. The point may arrive before the
// registration; the row is then created with an empty zone.
func (b *LocationBook) SaveGeometry(ctx context.Context, code string, x, y, z float64) error {
	_, err := queryFor(ctx, b.pool).Exec(ctx, `
		INSERT INTO locations (location_code, x_m, y_m, z_m) VALUES ($1, $2, $3, $4)
		ON CONFLICT (location_code) DO UPDATE
		SET x_m = EXCLUDED.x_m, y_m = EXCLUDED.y_m, z_m = EXCLUDED.z_m, updated_at = now()
	`, code, x, y, z)
	if err != nil {
		return fmt.Errorf("save geometry: %w", err)
	}
	return nil
}
