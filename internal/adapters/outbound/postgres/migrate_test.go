package postgres_test

import (
	"testing"

	"github.com/claudioed/task-interleaving/internal/adapters/outbound/postgres"
)

func TestMigrationsURLPrefersTheDirectConnection(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"migrations url wins", map[string]string{"MIGRATIONS_DATABASE_URL": "direct", "DATABASE_URL": "pooled"}, "direct"},
		{"falls back to the database url", map[string]string{"DATABASE_URL": "pooled"}, "pooled"},
		{"an empty migrations url falls back", map[string]string{"MIGRATIONS_DATABASE_URL": "", "DATABASE_URL": "pooled"}, "pooled"},
		{"nothing set", map[string]string{}, ""},
	}
	for _, c := range cases {
		if got := postgres.MigrationsURL(env(c.env)); got != c.want {
			t.Errorf("%s: MigrationsURL = %q, want %q", c.name, got, c.want)
		}
	}
}
