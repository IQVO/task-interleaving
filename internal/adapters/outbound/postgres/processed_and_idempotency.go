package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/task-interleaving/internal/application/repository"
)

// ProcessedEventRepo is the pgxpool-backed ports.ProcessedEvents (inbound
// consumer idempotency).
type ProcessedEventRepo struct {
	pool *pgxpool.Pool
}

// NewProcessedEventRepo constructs a ProcessedEventRepo over pool.
func NewProcessedEventRepo(pool *pgxpool.Pool) *ProcessedEventRepo {
	return &ProcessedEventRepo{pool: pool}
}

// Claim records (consumer, eventID) as processed, returning false if it was
// already recorded by an earlier call. Called inside a ports.UnitOfWork (the
// consumers' case) the INSERT is part of the SAME transaction as the event's
// effects, so a rollback un-claims it and a redelivery is processed, not
// skipped. A concurrent claim of the same key blocks on the unique index until
// the other transaction ends.
func (r *ProcessedEventRepo) Claim(ctx context.Context, consumer, eventID string) (bool, error) {
	tag, err := queryFor(ctx, r.pool).Exec(ctx, `
		INSERT INTO processed_events (consumer, event_id)
		VALUES ($1, $2)
		ON CONFLICT (consumer, event_id) DO NOTHING
	`, consumer, eventID)
	if err != nil {
		return false, fmt.Errorf("claim processed event: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// IdempotencyStore is the pgxpool-backed ports.IdempotencyStore.
type IdempotencyStore struct {
	pool *pgxpool.Pool
}

// NewIdempotencyStore constructs an IdempotencyStore over pool.
func NewIdempotencyStore(pool *pgxpool.Pool) *IdempotencyStore {
	return &IdempotencyStore{pool: pool}
}

// Lookup implements ports.IdempotencyStore: a row created before notBefore is
// outside the replay window and not found.
func (s *IdempotencyStore) Lookup(ctx context.Context, key string, notBefore time.Time) (repository.IdempotencyRecord, bool, error) {
	rec := repository.IdempotencyRecord{Key: key}
	err := queryFor(ctx, s.pool).QueryRow(ctx, `
		SELECT fingerprint, status, content_type, body, created_at
		FROM idempotency_keys WHERE key = $1 AND created_at >= $2
	`, key, notBefore.UTC()).Scan(&rec.Fingerprint, &rec.Status, &rec.ContentType, &rec.Body, &rec.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return repository.IdempotencyRecord{}, false, nil
	}
	if err != nil {
		return repository.IdempotencyRecord{}, false, fmt.Errorf("lookup idempotency key: %w", err)
	}
	rec.CreatedAt = rec.CreatedAt.UTC()
	return rec, true, nil
}

// Save implements ports.IdempotencyStore: the first writer of a key wins and a
// later Save of the same key changes nothing, unless the stored row was
// created before notBefore (outside the replay window): then it is replaced.
// A concurrent Save of the same key blocks on the key until the other
// transaction ends, then re-checks the WHERE against the row it committed.
func (s *IdempotencyStore) Save(ctx context.Context, rec repository.IdempotencyRecord, notBefore time.Time) error {
	_, err := queryFor(ctx, s.pool).Exec(ctx, `
		INSERT INTO idempotency_keys (key, fingerprint, status, content_type, body, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (key) DO UPDATE SET fingerprint = EXCLUDED.fingerprint, status = EXCLUDED.status,
			content_type = EXCLUDED.content_type, body = EXCLUDED.body, created_at = EXCLUDED.created_at
		WHERE idempotency_keys.created_at < $7
	`, rec.Key, rec.Fingerprint, rec.Status, rec.ContentType, nonNilBytes(rec.Body), rec.CreatedAt.UTC(), notBefore.UTC())
	if err != nil {
		return fmt.Errorf("save idempotency key: %w", err)
	}
	return nil
}

// nonNilBytes keeps a nil body from becoming SQL NULL (the column is NOT NULL).
func nonNilBytes(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}

// purgeExpiredKeysSQL deletes one bounded batch of the rows the replay read
// already ignores: created_at < notBefore is the exact complement of Lookup's
// created_at >= notBefore, the same notBefore the caller computes from
// IdempotencyWindow and its clock. The batch is picked oldest first with
// FOR UPDATE SKIP LOCKED, so a row a request holds (its unit of work inserted
// or is replacing the row and owns the row lock until commit) is skipped,
// never waited for, and no advisory lock is taken: the purge never waits on a
// request and a request never waits on the purge. The outer predicate
// re-checks the rows actually locked.
const purgeExpiredKeysSQL = `DELETE FROM idempotency_keys WHERE key IN (
	SELECT key FROM idempotency_keys WHERE created_at < $1
	ORDER BY created_at LIMIT $2 FOR UPDATE SKIP LOCKED
) AND created_at < $1`

// PurgeExpired implements ports.IdempotencyStore: up to limit rows created
// before notBefore are deleted, oldest first, and the count is returned. A
// row deleted here just before a request's Save turns that upsert into an
// insert; expiry itself stays enforced at read (ADR 0013, Amendment 2).
func (s *IdempotencyStore) PurgeExpired(ctx context.Context, notBefore time.Time, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	tag, err := queryFor(ctx, s.pool).Exec(ctx, purgeExpiredKeysSQL, notBefore.UTC(), limit)
	if err != nil {
		return 0, fmt.Errorf("purge expired idempotency keys: %w", err)
	}
	return int(tag.RowsAffected()), nil
}
