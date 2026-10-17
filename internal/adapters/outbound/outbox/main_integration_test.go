//go:build integration

package outbox_test

import (
	"os"
	"testing"

	"github.com/claudioed/task-interleaving/internal/testing/kafkatest"
	"github.com/claudioed/task-interleaving/internal/testing/pgtest"
)

// The infrastructure comes from testcontainers, never from the environment:
// internal/testing/pgtest boots ONE Postgres and internal/testing/kafkatest ONE
// Kafka per test binary (each lazily, on first use); TestMain only tears them
// down at the end.
func TestMain(m *testing.M) {
	code := m.Run()
	kafkatest.Shutdown()
	pgtest.Shutdown()
	os.Exit(code)
}
