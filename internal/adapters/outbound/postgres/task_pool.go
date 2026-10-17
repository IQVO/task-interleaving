package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/task-interleaving/internal/application/repository"
)

// TaskPool is the pgxpool-backed ports.TaskPool.
type TaskPool struct {
	pool *pgxpool.Pool
}

// NewTaskPool constructs a TaskPool over pool.
func NewTaskPool(pool *pgxpool.Pool) *TaskPool { return &TaskPool{pool: pool} }

const taskColumns = `task_id, task_type, site_id, from_location, to_location, required_capabilities, priority_band, state, claimed_by`

// Get implements ports.TaskPool.
func (p *TaskPool) Get(ctx context.Context, id string) (repository.Task, bool, error) {
	row := queryFor(ctx, p.pool).QueryRow(ctx, `SELECT `+taskColumns+` FROM task_pool WHERE task_id = $1`, id)
	t, err := scanTask(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return repository.Task{}, false, nil
	}
	if err != nil {
		return repository.Task{}, false, fmt.Errorf("get task: %w", err)
	}
	return t, true, nil
}

// Save implements ports.TaskPool.
func (p *TaskPool) Save(ctx context.Context, t repository.Task) error {
	_, err := queryFor(ctx, p.pool).Exec(ctx, `
		INSERT INTO task_pool (`+taskColumns+`) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (task_id) DO UPDATE
		SET task_type = EXCLUDED.task_type, site_id = EXCLUDED.site_id,
		    from_location = EXCLUDED.from_location, to_location = EXCLUDED.to_location,
		    required_capabilities = EXCLUDED.required_capabilities, priority_band = EXCLUDED.priority_band,
		    state = EXCLUDED.state, claimed_by = EXCLUDED.claimed_by, updated_at = now()
	`, t.ID, t.Type, t.SiteID, t.FromLocation, t.ToLocation, nonNilStrings(t.RequiredCapabilities),
		t.PriorityBand, string(t.State), t.ClaimedBy)
	if err != nil {
		return fmt.Errorf("save task: %w", err)
	}
	return nil
}

// Available implements ports.TaskPool.
func (p *TaskPool) Available(ctx context.Context, siteID string) ([]repository.Task, error) {
	rows, err := queryFor(ctx, p.pool).Query(ctx, `
		SELECT `+taskColumns+` FROM task_pool
		WHERE state = 'AVAILABLE' AND site_id = $1
		ORDER BY task_id
	`, siteID)
	if err != nil {
		return nil, fmt.Errorf("list available tasks: %w", err)
	}
	defer rows.Close()
	var out []repository.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, fmt.Errorf("scan task: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func scanTask(row pgx.Row) (repository.Task, error) {
	var (
		t     repository.Task
		state string
	)
	err := row.Scan(&t.ID, &t.Type, &t.SiteID, &t.FromLocation, &t.ToLocation,
		&t.RequiredCapabilities, &t.PriorityBand, &state, &t.ClaimedBy)
	t.State = repository.TaskState(state)
	return t, err
}

// nonNilStrings keeps a nil slice from becoming SQL NULL (the column is NOT NULL).
func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return slices.Clone(s)
}
