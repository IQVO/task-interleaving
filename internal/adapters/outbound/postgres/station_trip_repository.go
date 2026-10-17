package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/domain/policy"
	"github.com/claudioed/task-interleaving/internal/domain/recommend"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
	"github.com/claudioed/task-interleaving/internal/domain/travel"
)

// StationTripRepo is the pgxpool-backed ports.StationTripRepo. A trip is one
// station_trips row plus its most recent hint; every hint ever issued is kept
// in hints, with its candidates (hint_history.go).
type StationTripRepo struct {
	pool *pgxpool.Pool
}

// NewStationTripRepo constructs a StationTripRepo over pool.
func NewStationTripRepo(pool *pgxpool.Pool) *StationTripRepo { return &StationTripRepo{pool: pool} }

const tripColumns = `station_id, site_id, location_code, pos_x, pos_y, pos_z, last_task_id, last_completed_at, current_hint_id, version`

// Get implements ports.StationTripRepo.
func (r *StationTripRepo) Get(ctx context.Context, id stationtrip.StationID) (*stationtrip.StationTrip, error) {
	q := queryFor(ctx, r.pool)
	snap, hintID, err := scanTrip(q.QueryRow(ctx, `SELECT `+tripColumns+` FROM station_trips WHERE station_id = $1`, string(id)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, repository.ErrStationNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get station trip: %w", err)
	}
	if hintID != "" {
		rec, err := hintByID(ctx, q, stationtrip.HintID(hintID))
		if err != nil {
			return nil, fmt.Errorf("get current hint of station %s: %w", id, err)
		}
		snap.Hint = &rec.Hint
	}
	return stationtrip.Restore(snap)
}

// scanTrip reads one station_trips row into a snapshot without its hint, and
// the id of the current hint ("" when none).
func scanTrip(row pgx.Row) (stationtrip.Snapshot, string, error) {
	var (
		station, site, location, lastTask string
		x, y, z                           *float64
		lastAt                            *time.Time
		hintID                            *string
		version                           int64
	)
	if err := row.Scan(&station, &site, &location, &x, &y, &z, &lastTask, &lastAt, &hintID, &version); err != nil {
		return stationtrip.Snapshot{}, "", err
	}
	place := travel.Place{Location: location}
	if x != nil && y != nil && z != nil {
		place.Point = &travel.Point{X: *x, Y: *y, Z: *z}
	}
	snap := stationtrip.Snapshot{
		ID:       stationtrip.StationID(station),
		Position: stationtrip.Position{Site: policy.SiteID(site), Place: place},
		LastTask: recommend.TaskID(lastTask),
		Version:  version,
	}
	if lastAt != nil {
		snap.LastAt = lastAt.UTC()
	}
	if hintID == nil {
		return snap, "", nil
	}
	return snap, *hintID, nil
}

// Save implements ports.StationTripRepo. The trip row is written first (its
// version guard serialises the writers of one station), then the hint history.
func (r *StationTripRepo) Save(ctx context.Context, trip *stationtrip.StationTrip, loadedVersion int64) error {
	snap := trip.Snapshot()
	return inTx(ctx, r.pool, func(q querier) error {
		if err := saveTrip(ctx, q, snap, loadedVersion); err != nil {
			return err
		}
		if snap.Hint == nil {
			return nil
		}
		return recordHint(ctx, q, snap)
	})
}

func tripArgs(s stationtrip.Snapshot) []any {
	var x, y, z *float64
	if p := s.Position.Place.Point; p != nil {
		x, y, z = &p.X, &p.Y, &p.Z
	}
	var lastAt *time.Time
	if !s.LastAt.IsZero() {
		at := s.LastAt.UTC()
		lastAt = &at
	}
	var hintID *string
	if s.Hint != nil {
		id := string(s.Hint.ID)
		hintID = &id
	}
	return []any{
		string(s.ID), string(s.Position.Site), s.Position.Place.Location, x, y, z,
		string(s.LastTask), lastAt, hintID, s.Version,
	}
}

func saveTrip(ctx context.Context, q querier, s stationtrip.Snapshot, loadedVersion int64) error {
	if loadedVersion == 0 {
		_, err := q.Exec(ctx, `
			INSERT INTO station_trips (`+tripColumns+`) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		`, tripArgs(s)...)
		if isUniqueViolation(err, "station_trips_pkey") {
			return repository.ErrConcurrentModification
		}
		if err != nil {
			return fmt.Errorf("insert station trip: %w", err)
		}
		return nil
	}
	tag, err := q.Exec(ctx, `
		UPDATE station_trips
		SET site_id = $2, location_code = $3, pos_x = $4, pos_y = $5, pos_z = $6, last_task_id = $7,
		    last_completed_at = $8, current_hint_id = $9, version = $10, updated_at = now()
		WHERE station_id = $1 AND version = $11
	`, append(tripArgs(s), loadedVersion)...)
	if err != nil {
		return fmt.Errorf("update station trip: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return repository.ErrConcurrentModification
	}
	return nil
}

// ExpiredActive implements ports.StationTripRepo.
func (r *StationTripRepo) ExpiredActive(ctx context.Context, now time.Time, limit int) ([]stationtrip.StationID, error) {
	rows, err := queryFor(ctx, r.pool).Query(ctx, `
		SELECT station_id FROM hints
		WHERE state = 'Active' AND expires_at <= $1
		ORDER BY station_id
		LIMIT $2
	`, now.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("list expired hints: %w", err)
	}
	defer rows.Close()
	var ids []stationtrip.StationID
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan station id: %w", err)
		}
		ids = append(ids, stationtrip.StationID(id))
	}
	return ids, rows.Err()
}
