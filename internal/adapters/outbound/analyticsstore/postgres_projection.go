package analyticsstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/task-interleaving/internal/analytics/report"
)

// Projection is the Postgres WRITER (report.Projection). Apply claims the event
// id and folds the event into the fact table in ONE transaction: the id is
// recorded if and only if its effect is, so a failure anywhere leaves nothing
// behind and the redelivered event is applied afresh, while a replay of an
// applied id is a no-op.
type Projection struct {
	pool *pgxpool.Pool
}

// NewProjection constructs a Projection over the writer pool.
func NewProjection(pool *pgxpool.Pool) *Projection { return &Projection{pool: pool} }

var _ report.Projection = (*Projection)(nil)

const claimSQL = `
	INSERT INTO analytics_processed_events (event_id, event_type, occurred_at)
	VALUES ($1, $2, $3)
	ON CONFLICT (event_id) DO NOTHING`

// Every fact upsert keeps the EARLIEST candidate of each milestone: LEAST
// ignores NULLs, so a NULL parameter (the event does not mark that milestone)
// keeps the stored value, and the row converges whatever order the events
// arrive in. The attributes of an event (expiry and candidate count of the
// issue, rank and detour of the consumption) are first-writer-wins.
const upsertHintSQL = `
	INSERT INTO hint_facts (hint_id, station_id, site_id, issued_at, expires_at, candidate_count,
	                        consumed_at, consumed_rank, consumed_detour_m, expired_at)
	VALUES ($1, $2, $3, $4::timestamptz, $5::timestamptz, $6::integer, $7::timestamptz, $8::integer, $9::float8, $10::timestamptz)
	ON CONFLICT (hint_id) DO UPDATE SET
		issued_at         = LEAST(hint_facts.issued_at, EXCLUDED.issued_at),
		expires_at        = COALESCE(hint_facts.expires_at, EXCLUDED.expires_at),
		candidate_count   = COALESCE(hint_facts.candidate_count, EXCLUDED.candidate_count),
		consumed_at       = LEAST(hint_facts.consumed_at, EXCLUDED.consumed_at),
		consumed_rank     = COALESCE(hint_facts.consumed_rank, EXCLUDED.consumed_rank),
		consumed_detour_m = COALESCE(hint_facts.consumed_detour_m, EXCLUDED.consumed_detour_m),
		expired_at        = LEAST(hint_facts.expired_at, EXCLUDED.expired_at)`

// Apply implements report.Projection.
func (p *Projection) Apply(ctx context.Context, e report.Event) (bool, error) {
	if err := e.Validate(); err != nil {
		// A pure check of the decoded event: deterministic, the same bytes
		// always fail (the consumer dead-letters it).
		return false, fmt.Errorf("%w: %w", report.ErrRejected, err)
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("analyticsstore: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed

	tag, err := tx.Exec(ctx, claimSQL, e.EventID, string(e.Kind), e.At.UTC())
	if err != nil {
		return false, classify(fmt.Errorf("analyticsstore: claim event %s: %w", e.EventID, err))
	}
	if tag.RowsAffected() == 0 {
		return false, nil // already applied: the deferred rollback ends the empty tx
	}
	if err := fold(ctx, tx, e); err != nil {
		return false, classify(fmt.Errorf("analyticsstore: project %s %s: %w", e.Kind, e.EventID, err))
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("analyticsstore: commit: %w", err)
	}
	return true, nil
}

// fold writes the facts of one event inside tx. A policy declaration has no
// fact: its id in analytics_processed_events is its whole effect (freshness).
func fold(ctx context.Context, tx pgx.Tx, e report.Event) error {
	if !e.Kind.IsHint() {
		return nil
	}
	m := report.MilestonesOf(e)
	var (
		expires        any
		candidateCount any
		rank           any
		detour         any
	)
	switch e.Kind {
	case report.KindHintIssued:
		expires, candidateCount = e.ExpiresAt.UTC(), e.CandidateCount
	case report.KindHintConsumed:
		rank, detour = e.Rank, e.DetourM
	case report.KindHintExpired, report.KindPolicyDeclared:
		// no attributes beyond the milestone
	}
	_, err := tx.Exec(ctx, upsertHintSQL, e.HintID, e.Station, e.SiteID,
		m.Issued, expires, candidateCount, m.Consumed, rank, detour, m.Expired)
	return err
}

// classify wraps a Postgres data-exception (SQLSTATE class 22) or
// integrity-violation (class 23) error in report.ErrRejected: the same event can
// never succeed, so the consumer dead-letters it instead of retrying forever.
// Everything else (connection loss, timeouts, locks, failovers) stays transient.
func classify(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && len(pgErr.Code) >= 2 && (pgErr.Code[:2] == "22" || pgErr.Code[:2] == "23") {
		return fmt.Errorf("%w: %w", report.ErrRejected, err)
	}
	return err
}
