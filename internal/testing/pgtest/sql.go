package pgtest

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Exec runs one statement directly on pool, bypassing the repositories, so a
// test can exercise the database's own rules.
func Exec(pool *pgxpool.Pool, sql string, args ...any) error {
	_, err := pool.Exec(context.Background(), sql, args...)
	return err
}

// IndexDef returns the definition of the named index (pg_indexes.indexdef).
func IndexDef(t *testing.T, pool *pgxpool.Pool, name string) string {
	t.Helper()
	var def string
	err := pool.QueryRow(context.Background(), `SELECT indexdef FROM pg_indexes WHERE indexname = $1`, name).Scan(&def)
	if err != nil {
		t.Fatalf("index %s must exist: %v", name, err)
	}
	return def
}

// IsUniqueViolation reports whether err is a unique violation of the named
// constraint or index.
func IsUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}
