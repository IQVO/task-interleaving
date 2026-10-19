package analyticsstore

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/task-interleaving/internal/analytics/report"
)

// Reader is the Postgres READER (report.Reader). Days are UTC calendar days;
// every window is half-open [lo, hi). SQL counts and takes percentiles
// (percentile_cont, the definition report.Percentile mirrors); the range rules,
// the site scope, the adoption rate and the freshness lag live in
// internal/analytics/report.
type Reader struct {
	pool *pgxpool.Pool
}

// NewReader constructs a Reader over the (read-only) reports pool.
func NewReader(pool *pgxpool.Pool) *Reader { return &Reader{pool: pool} }

var _ report.Reader = (*Reader)(nil)

// adoptionDaysSQL builds the same windows as report.DayWindows: one per UTC day
// intersecting [$1, $2), clipped to the range, optionally narrowed to one site
// ($3, ” = every site). The day series runs over UTC-naive timestamps so a
// session time zone with DST cannot shift a day. A negative duration (events
// recorded out of order) counts as zero.
const adoptionDaysSQL = `
	WITH days AS (
		SELECT d::date AS day,
		       GREATEST(d AT TIME ZONE 'UTC', $1::timestamptz)                    AS lo,
		       LEAST((d + interval '1 day') AT TIME ZONE 'UTC', $2::timestamptz) AS hi
		FROM generate_series(
		         date_trunc('day', $1::timestamptz AT TIME ZONE 'UTC'),
		         ($2::timestamptz - interval '1 microsecond') AT TIME ZONE 'UTC',
		         interval '1 day') AS d
		WHERE $1::timestamptz < $2::timestamptz
	)
	SELECT days.day,
	       (SELECT count(*) FROM hint_facts h WHERE ($3 = '' OR h.site_id = $3) AND h.issued_at >= days.lo AND h.issued_at < days.hi),
	       consumed.n, consumed.first_rank, consumed.mean_detour,
	       (SELECT count(*) FROM hint_facts h WHERE ($3 = '' OR h.site_id = $3) AND h.expired_at >= days.lo AND h.expired_at < days.hi),
	       ttc.n, ttc.p50, ttc.p95
	FROM days
	CROSS JOIN LATERAL (
		SELECT count(*) AS n,
		       count(*) FILTER (WHERE h.consumed_rank = 1) AS first_rank,
		       avg(h.consumed_detour_m)::float8 AS mean_detour
		FROM hint_facts h
		WHERE ($3 = '' OR h.site_id = $3) AND h.consumed_at >= days.lo AND h.consumed_at < days.hi
	) consumed
	CROSS JOIN LATERAL (
		SELECT count(*) AS n,
		       percentile_cont(0.50) WITHIN GROUP (ORDER BY s.secs) AS p50,
		       percentile_cont(0.95) WITHIN GROUP (ORDER BY s.secs) AS p95
		FROM (SELECT GREATEST(0, EXTRACT(EPOCH FROM h.consumed_at - h.issued_at))::float8 AS secs
		      FROM hint_facts h
		      WHERE ($3 = '' OR h.site_id = $3) AND h.issued_at IS NOT NULL AND h.consumed_at >= days.lo AND h.consumed_at < days.hi) s
	) ttc
	ORDER BY days.day`

// AdoptionDays implements report.Reader.
func (r *Reader) AdoptionDays(ctx context.Context, q report.AdoptionQuery) ([]report.AdoptionDay, error) {
	rows, err := r.pool.Query(ctx, adoptionDaysSQL, q.Range.From.UTC(), q.Range.To.UTC(), q.SiteID)
	if err != nil {
		return nil, fmt.Errorf("analyticsstore: adoption days: %w", err)
	}
	defer rows.Close()
	out := []report.AdoptionDay{}
	for rows.Next() {
		var d report.AdoptionDay
		if err := rows.Scan(&d.Day, &d.Issued, &d.Consumed, &d.ConsumedFirstRank, &d.MeanDetourM, &d.Expired,
			&d.TimeToConsume.Count, &d.TimeToConsume.P50, &d.TimeToConsume.P95); err != nil {
			return nil, fmt.Errorf("analyticsstore: scan adoption day: %w", err)
		}
		y, m, dd := d.Day.Date()
		d.Day = time.Date(y, m, dd, 0, 0, 0, 0, time.UTC)
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("analyticsstore: adoption days rows: %w", err)
	}
	return out, nil
}

// LastEventAt implements report.Reader: the newest CloudEvents time among the
// applied events, nil while none was applied.
func (r *Reader) LastEventAt(ctx context.Context) (*time.Time, error) {
	var at *time.Time
	if err := r.pool.QueryRow(ctx, `SELECT max(occurred_at) FROM analytics_processed_events`).Scan(&at); err != nil {
		return nil, fmt.Errorf("analyticsstore: last event: %w", err)
	}
	if at != nil {
		utc := at.UTC()
		at = &utc
	}
	return at, nil
}
