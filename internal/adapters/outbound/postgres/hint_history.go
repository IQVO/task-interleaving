package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/domain/policy"
	"github.com/claudioed/task-interleaving/internal/domain/recommend"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
)

// activeHintIndex is the partial unique index behind invariant 2 (ADR 0002,
// ADR 0005): at most one Active hint per station.
const activeHintIndex = "uq_hints_one_active_per_station"

const hintColumns = `hint_id, station_id, site_id, after_task_id, origin_location, issued_at, expires_at,
	policy_version, state, consumed_task_id, consumed_at, superseded_at, version`

// recordHint keeps the hint history in step with the trip's current hint: a
// known hint that changed state moves on; a hint seen for the first time
// supersedes the station's Active one and is inserted with its candidates.
func recordHint(ctx context.Context, q querier, snap stationtrip.Snapshot) error {
	h := *snap.Hint
	moved, err := moveHint(ctx, q, h)
	if err != nil || moved {
		return err
	}
	var known bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hints WHERE hint_id = $1)`, string(h.ID)).Scan(&known); err != nil {
		return fmt.Errorf("check hint %s: %w", h.ID, err)
	}
	if known {
		return nil
	}
	return insertHint(ctx, q, snap)
}

// consumption returns the consumed task and instant of a Consumed hint, nil
// for every other state.
func consumption(h stationtrip.Hint) (*string, *time.Time) {
	if h.State != stationtrip.StateConsumed {
		return nil, nil
	}
	task, at := string(h.ConsumedTask), h.ConsumedAt.UTC()
	return &task, &at
}

// moveHint applies a state change of a hint already in the history (version +
// 1) and reports whether the hint changed. The consumed task and instant are
// set exactly when the hint is Consumed.
func moveHint(ctx context.Context, q querier, h stationtrip.Hint) (bool, error) {
	task, at := consumption(h)
	tag, err := q.Exec(ctx, `
		UPDATE hints
		SET state = $2, consumed_task_id = $3, consumed_at = $4, version = version + 1, updated_at = now()
		WHERE hint_id = $1 AND state <> $2
	`, string(h.ID), string(h.State), task, at)
	if err != nil {
		return false, fmt.Errorf("move hint %s: %w", h.ID, err)
	}
	return tag.RowsAffected() == 1, nil
}

func insertHint(ctx context.Context, q querier, snap stationtrip.Snapshot) error {
	h := *snap.Hint
	// The previous Active hint of the station is closed first: the unique
	// index admits one Active hint, and the newer one replaces it.
	if _, err := q.Exec(ctx, `
		UPDATE hints
		SET state = 'Superseded', superseded_at = $2, version = version + 1, updated_at = now()
		WHERE station_id = $1 AND state = 'Active'
	`, string(snap.ID), h.IssuedAt.UTC()); err != nil {
		return fmt.Errorf("supersede the active hint of station %s: %w", snap.ID, err)
	}
	task, at := consumption(h)
	_, err := q.Exec(ctx, `
		INSERT INTO hints (`+hintColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, NULL, 1)
	`, string(h.ID), string(snap.ID), string(snap.Position.Site), string(h.AfterTask), h.Origin,
		h.IssuedAt.UTC(), h.ExpiresAt.UTC(), h.PolicyVersion, string(h.State), task, at)
	if isUniqueViolation(err, activeHintIndex) || isUniqueViolation(err, "hints_pkey") {
		return repository.ErrConcurrentModification
	}
	if err != nil {
		return fmt.Errorf("insert hint %s: %w", h.ID, err)
	}
	return insertCandidates(ctx, q, h)
}

func insertCandidates(ctx context.Context, q querier, h stationtrip.Hint) error {
	n := len(h.Candidates)
	ranks, tasks := make([]int, n), make([]string, n)
	types, locations, detours := make([]string, n), make([]string, n), make([]float64, n)
	for i, c := range h.Candidates {
		ranks[i], tasks[i], types[i] = c.Rank, string(c.TaskID), string(c.TaskType)
		locations[i], detours[i] = c.Location, c.DetourM
	}
	_, err := q.Exec(ctx, `
		INSERT INTO candidates (hint_id, rank, task_id, task_type, location, detour_m)
		SELECT $1, rank, task_id, task_type, location, detour_m
		FROM unnest($2::int[], $3::text[], $4::text[], $5::text[], $6::float8[])
		     AS c (rank, task_id, task_type, location, detour_m)
	`, string(h.ID), ranks, tasks, types, locations, detours)
	if err != nil {
		return fmt.Errorf("insert candidates of hint %s: %w", h.ID, err)
	}
	return nil
}

// GetHint implements ports.StationTripRepo.
func (r *StationTripRepo) GetHint(ctx context.Context, id stationtrip.HintID) (repository.HintRecord, error) {
	return hintByID(ctx, queryFor(ctx, r.pool), id)
}

func hintByID(ctx context.Context, q querier, id stationtrip.HintID) (repository.HintRecord, error) {
	recs, err := queryHints(ctx, q, `SELECT `+hintColumns+` FROM hints WHERE hint_id = $1`, string(id))
	if err != nil {
		return repository.HintRecord{}, err
	}
	if len(recs) == 0 {
		return repository.HintRecord{}, repository.ErrHintNotFound
	}
	return recs[0], nil
}

// ListHints implements ports.StationTripRepo. An unknown cursor yields no rows.
func (r *StationTripRepo) ListHints(ctx context.Context, f repository.HintFilter, after stationtrip.HintID, limit int) ([]repository.HintRecord, error) {
	return queryHints(ctx, queryFor(ctx, r.pool), `
		SELECT `+hintColumns+` FROM hints
		WHERE ($1 = '' OR station_id = $1)
		  AND ($2 = '' OR site_id = $2)
		  AND ($3 = '' OR state = $3)
		  AND ($4 = '' OR (issued_at, hint_id) < (
		        SELECT a.issued_at, a.hint_id FROM hints a WHERE a.hint_id = $4))
		ORDER BY issued_at DESC, hint_id DESC
		LIMIT $5
	`, string(f.StationID), string(f.SiteID), string(f.State), string(after), limit)
}

// queryHints runs a query selecting hintColumns and attaches the candidates of
// every hint it returned.
func queryHints(ctx context.Context, q querier, sql string, args ...any) ([]repository.HintRecord, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("query hints: %w", err)
	}
	defer rows.Close()
	var recs []repository.HintRecord
	var ids []string
	for rows.Next() {
		rec, err := scanHint(rows)
		if err != nil {
			return nil, fmt.Errorf("scan hint: %w", err)
		}
		recs = append(recs, rec)
		ids = append(ids, string(rec.Hint.ID))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	return withCandidates(ctx, q, recs, ids)
}

func withCandidates(ctx context.Context, q querier, recs []repository.HintRecord, ids []string) ([]repository.HintRecord, error) {
	if len(recs) == 0 {
		return recs, nil
	}
	byHint, err := loadCandidates(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	for i := range recs {
		recs[i].Hint.Candidates = byHint[string(recs[i].Hint.ID)]
	}
	return recs, nil
}

func loadCandidates(ctx context.Context, q querier, ids []string) (map[string][]recommend.Candidate, error) {
	rows, err := q.Query(ctx, `
		SELECT hint_id, rank, task_id, task_type, location, detour_m
		FROM candidates WHERE hint_id = ANY($1)
		ORDER BY hint_id, rank
	`, ids)
	if err != nil {
		return nil, fmt.Errorf("query candidates: %w", err)
	}
	defer rows.Close()
	out := map[string][]recommend.Candidate{}
	for rows.Next() {
		var (
			hint, task, typ string
			c               recommend.Candidate
		)
		if err := rows.Scan(&hint, &c.Rank, &task, &typ, &c.Location, &c.DetourM); err != nil {
			return nil, fmt.Errorf("scan candidate: %w", err)
		}
		c.TaskID, c.TaskType = recommend.TaskID(task), policy.TaskType(typ)
		out[hint] = append(out[hint], c)
	}
	return out, rows.Err()
}

func scanHint(row pgx.Row) (repository.HintRecord, error) {
	var (
		rec                                     repository.HintRecord
		id, station, site, after, origin, state string
		issued, expires                         time.Time
		consumedTask                            *string
		consumedAt, supersededAt                *time.Time
	)
	err := row.Scan(&id, &station, &site, &after, &origin, &issued, &expires,
		&rec.Hint.PolicyVersion, &state, &consumedTask, &consumedAt, &supersededAt, &rec.Version)
	if err != nil {
		return repository.HintRecord{}, err
	}
	rec.StationID, rec.SiteID = stationtrip.StationID(station), policy.SiteID(site)
	rec.Hint.ID, rec.Hint.AfterTask, rec.Hint.Origin = stationtrip.HintID(id), recommend.TaskID(after), origin
	rec.Hint.IssuedAt, rec.Hint.ExpiresAt = issued.UTC(), expires.UTC()
	rec.Hint.State = stationtrip.HintState(state)
	if consumedTask != nil {
		rec.Hint.ConsumedTask = recommend.TaskID(*consumedTask)
	}
	if consumedAt != nil {
		rec.Hint.ConsumedAt = consumedAt.UTC()
	}
	if supersededAt != nil {
		rec.SupersededAt = supersededAt.UTC()
	}
	return rec, nil
}
