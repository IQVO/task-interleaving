package postgres

import (
	"context"
	"errors"
	"fmt"

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

// Lookup implements ports.IdempotencyStore.
func (s *IdempotencyStore) Lookup(ctx context.Context, key string) (repository.IdempotencyRecord, bool, error) {
	rec := repository.IdempotencyRecord{Key: key}
	err := queryFor(ctx, s.pool).QueryRow(ctx, `
		SELECT fingerprint, status, content_type, body, created_at
		FROM idempotency_keys WHERE key = $1
	`, key).Scan(&rec.Fingerprint, &rec.Status, &rec.ContentType, &rec.Body, &rec.CreatedAt)
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
// later Save of the same key changes nothing.
func (s *IdempotencyStore) Save(ctx context.Context, rec repository.IdempotencyRecord) error {
	_, err := queryFor(ctx, s.pool).Exec(ctx, `
		INSERT INTO idempotency_keys (key, fingerprint, status, content_type, body, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (key) DO NOTHING
	`, rec.Key, rec.Fingerprint, rec.Status, rec.ContentType, nonNilBytes(rec.Body), rec.CreatedAt.UTC())
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
