package pgtest

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// WaitAllPublished fails the test unless every outbox row has been published
// within a few seconds.
func WaitAllPublished(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	var unpublished int
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM outbox_events WHERE published_at IS NULL`).Scan(&unpublished); err != nil {
			t.Fatal(err)
		}
		if unpublished == 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%d outbox rows still unpublished", unpublished)
}

// InsertOutboxRow writes one unpublished outbox_events row directly (topic
// test.integration, a stub value, no stored headers) and returns its id, so
// relay tests control event_type and key exactly.
func InsertOutboxRow(t *testing.T, pool *pgxpool.Pool, eventType, key string) int64 {
	t.Helper()
	var id int64
	err := pool.QueryRow(context.Background(), `
		INSERT INTO outbox_events (event_id, topic, event_type, subject, key, dataschema, value)
		VALUES (gen_random_uuid()::text, 'test.integration', $1, $2, $3, 'urn:test', '{"stub":true}')
		RETURNING id
	`, eventType, key, []byte(key)).Scan(&id)
	if err != nil {
		t.Fatalf("insert outbox row: %v", err)
	}
	return id
}

// OutboxRowState returns the attempts, last_error and published flag of one
// outbox_events row.
func OutboxRowState(t *testing.T, pool *pgxpool.Pool, id int64) (attempts int, lastErr string, published bool) {
	t.Helper()
	err := pool.QueryRow(context.Background(), `
		SELECT attempts, coalesce(last_error, ''), published_at IS NOT NULL
		FROM outbox_events WHERE id = $1
	`, id).Scan(&attempts, &lastErr, &published)
	if err != nil {
		t.Fatalf("read outbox row %d: %v", id, err)
	}
	return attempts, lastErr, published
}
