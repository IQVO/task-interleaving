package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/domain/policy"
)

// PolicyRepo is the pgxpool-backed ports.PolicyRepo.
type PolicyRepo struct {
	pool *pgxpool.Pool
}

// NewPolicyRepo constructs a PolicyRepo over pool.
func NewPolicyRepo(pool *pgxpool.Pool) *PolicyRepo { return &PolicyRepo{pool: pool} }

const policyColumns = `site_id, max_detour_m, max_candidates, hint_ttl_ns, eligible_task_types, vertical_weight, version, declared_at`

// Get implements ports.PolicyRepo.
func (r *PolicyRepo) Get(ctx context.Context, site policy.SiteID) (repository.PolicyRecord, error) {
	row := queryFor(ctx, r.pool).QueryRow(ctx,
		`SELECT `+policyColumns+` FROM interleaving_policies WHERE site_id = $1`, string(site))
	rec, err := scanPolicy(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return repository.PolicyRecord{}, repository.ErrPolicyNotFound
	}
	return rec, err
}

// Save implements ports.PolicyRepo.
func (r *PolicyRepo) Save(ctx context.Context, rec repository.PolicyRecord, loadedVersion int64) error {
	if loadedVersion == 0 {
		return r.insert(ctx, rec)
	}
	return r.update(ctx, rec, loadedVersion)
}

func policyArgs(rec repository.PolicyRecord) []any {
	p := rec.Policy
	types := make([]string, 0, len(p.EligibleTaskTypes()))
	for _, t := range p.EligibleTaskTypes() {
		types = append(types, string(t))
	}
	return []any{
		string(p.SiteID()), p.MaxDetourM(), p.MaxCandidates(), int64(p.HintTTL()),
		types, p.VerticalWeight(), p.Version(), rec.DeclaredAt.UTC(),
	}
}

func (r *PolicyRepo) insert(ctx context.Context, rec repository.PolicyRecord) error {
	_, err := queryFor(ctx, r.pool).Exec(ctx, `
		INSERT INTO interleaving_policies (`+policyColumns+`) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, policyArgs(rec)...)
	if isUniqueViolation(err, "interleaving_policies_pkey") {
		return repository.ErrConcurrentModification
	}
	if err != nil {
		return fmt.Errorf("insert interleaving policy: %w", err)
	}
	return nil
}

func (r *PolicyRepo) update(ctx context.Context, rec repository.PolicyRecord, loadedVersion int64) error {
	args := append(policyArgs(rec), loadedVersion)
	tag, err := queryFor(ctx, r.pool).Exec(ctx, `
		UPDATE interleaving_policies
		SET max_detour_m = $2, max_candidates = $3, hint_ttl_ns = $4, eligible_task_types = $5,
		    vertical_weight = $6, version = $7, declared_at = $8
		WHERE site_id = $1 AND version = $9
	`, args...)
	if err != nil {
		return fmt.Errorf("update interleaving policy: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return repository.ErrConcurrentModification
	}
	return nil
}

func scanPolicy(row pgx.Row) (repository.PolicyRecord, error) {
	var (
		site       string
		s          policy.Settings
		ttlNs      int64
		types      []string
		version    int64
		declaredAt time.Time
	)
	if err := row.Scan(&site, &s.MaxDetourM, &s.MaxCandidates, &ttlNs, &types, &s.VerticalWeight, &version, &declaredAt); err != nil {
		return repository.PolicyRecord{}, err
	}
	s.HintTTL = time.Duration(ttlNs)
	for _, t := range types {
		s.EligibleTaskTypes = append(s.EligibleTaskTypes, policy.TaskType(t))
	}
	p, err := policy.Restore(policy.SiteID(site), s, version)
	if err != nil {
		return repository.PolicyRecord{}, fmt.Errorf("stored policy of site %s is invalid: %w", site, err)
	}
	return repository.PolicyRecord{Policy: p, DeclaredAt: declaredAt.UTC()}, nil
}

// isUniqueViolation reports whether err is a unique-constraint violation of
// the named constraint or index.
func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}
