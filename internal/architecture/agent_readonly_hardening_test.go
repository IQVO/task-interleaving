package architecture

import (
	"strings"
	"testing"
)

// Hardened agent read-only sensor (fleet audit 2026-11-21, defect 2; harness
// templates/architecture/agent_readonly_*.tmpl, develop ea62687). Every "bad"
// fixture below passed this repo's mcp_readonly sensor (local-name match on
// `usecases`, mcp_readonly_fitness_test.go 120/140), and the "good" wiring was
// a false positive there:
//
//	G1 a qualifier was matched by its LOCAL name, so an alias laundered a write seam;
//	G2 a dot or aliased import of a guarded package was not refused;
//	G3 a write METHOD on a value an allowlisted constructor returned got through
//	   (postgres was not guarded at all);
//	G4 the pgx pool's raw-SQL methods (Query, QueryRow, Exec, Begin, Acquire ...) got through;
//	G5 a raw driver import (pgx, database/sql) was not refused.
//
// The fixtures use only the sensor's entry point (agentSourceViolations), so
// the RED on the old sensor is an assertion, not a compile error.

// agentWriteVerbsWant pins agentWriteVerbs from the test side: the fleet's
// repo, unit-of-work and pool write methods and raw-SQL entry points, plus this
// module's own write methods on the postgres repos and use-case seams an agent
// source can reach (the location book, the processed-event claim, the event
// sink, the unit of work and the outbox drain).
var agentWriteVerbsWant = []string{"Save", "Insert", "Update", "Delete", "Lock", "Purge", "Append", "Enqueue",
	"Exec", "Begin", "BeginTx", "CopyFrom", "SendBatch",
	"Query", "QueryRow", "Acquire", "AcquireFunc", "AcquireAllIdle", "Conn", "PgConn", "ExecParams", "Hijack",
	"SaveZone", "SaveLocation", "SaveGeometry", "DecommissionLocation", "Claim", "Do", "Drain",
	"PublishPolicyDeclared", "PublishTripEvents"}

func hardeningSurface(t *testing.T, name string) agentSurface {
	t.Helper()
	for _, s := range agentSurfaces {
		if s.name == name {
			return s
		}
	}
	t.Fatalf("no %s surface configured", name)
	return agentSurface{}
}

func TestAgentReadOnlySensorResolvesQualifiersByImportPath(t *testing.T) {
	uc := "\"" + modulePath + "/internal/application/usecases\""
	pg := "\"" + modulePath + "/internal/adapters/outbound/postgres\""
	bad := map[string]string{
		// G1: the allowlist resolves a qualifier through the file's imports.
		"an aliased write use case": "package x\nimport u " + uc + "\nvar _ = &u.DeclarePolicy{}\n",
		"an aliased sweeper":        "package x\nimport u " + uc + "\nvar _ *u.ExpireHints\n",
		"an aliased trip writer":    "package x\nimport u " + uc + "\nvar _ = u.TripWriter{}\n",
		"an aliased unit of work":   "package x\nimport p " + pg + "\nvar _ = p.NewUnitOfWork\n",
		"an aliased outbox repo":    "package x\nimport p " + pg + "\nvar _ = p.NewOutboxRepo\n",
		"an aliased idempotency":    "package x\nimport p " + pg + "\nvar _ = p.NewIdempotencyStore\n",
		// G1 without an alias: postgres write constructors were not guarded at all.
		"the unit of work":      "package x\nimport " + pg + "\nvar _ = postgres.NewUnitOfWork\n",
		"the outbox repo":       "package x\nimport " + pg + "\nvar _ = postgres.NewOutboxRepo\n",
		"the location book":     "package x\nimport " + pg + "\nvar _ = postgres.NewLocationBook\n",
		"the processed claim":   "package x\nimport " + pg + "\nvar _ = postgres.NewProcessedEventRepo\n",
		"the idempotency store": "package x\nimport " + pg + "\nvar _ = postgres.NewIdempotencyStore\n",
		"the task pool":         "package x\nimport " + pg + "\nvar _ = postgres.NewTaskPool\n",
		// G2: a dot or aliased import of a guarded package is itself refused.
		"an aliased guarded import": "package x\nimport u " + uc + "\nvar _ = &u.GetPolicy{}\n",
		"a dot import":              "package x\nimport . " + uc + "\nvar _ = &DeclarePolicy{}\n",
		"a dot import of postgres":  "package x\nimport . " + pg + "\nvar _ = NewUnitOfWork\n",
	}
	for _, surface := range []string{"mcp", "a2a"} {
		s := hardeningSurface(t, surface)
		for name, src := range bad {
			if got, err := agentSourceViolations(s, "fixture.go", src); err != nil || len(got) == 0 {
				t.Errorf("%s: the sensor does not flag %s (violations=%v, err=%v)", surface, name, got, err)
			}
		}
	}
	// cmd/mcp's own wiring: an aliased NON-guarded package, the read use cases
	// and read constructors, pool.Close/Ping; and a guarded local NAME bound
	// to another import path is not that package.
	wiring := "package x\nimport inboundmcp \"" + modulePath + "/internal/adapters/inbound/mcp\"\n" +
		"import " + pg + "\nimport " + uc + "\n" +
		"func f() { _ = postgres.RunMigrations(dsn); pool, _ := postgres.NewPool(ctx, dsn); defer pool.Close(); _ = pool.Ping(ctx)\n" +
		"policies, trips := postgres.NewPolicyRepo(pool), postgres.NewStationTripRepo(pool)\n" +
		"_ = inboundmcp.NewServer(inboundmcp.Deps{GetPolicy: &usecases.GetPolicy{Policies: policies}, ListHints: &usecases.ListHints{Trips: trips},\n" +
		"GetHint: &usecases.GetHint{Trips: trips}, GetStation: &usecases.GetStation{Trips: trips}}) }\n"
	elsewhere := "package x\nimport usecases \"example.com/elsewhere/usecases\"\nvar _ = usecases.DeclarePolicy\n"
	for name, src := range map[string]string{"cmd/mcp's read wiring": wiring, "a local name usecases bound to another path": elsewhere} {
		if got, err := agentSourceViolations(hardeningSurface(t, "mcp"), "fixture.go", src); err != nil || len(got) != 0 {
			t.Errorf("false positive on %s: %v (err=%v)", name, got, err)
		}
	}
}

func TestAgentReadOnlySensorRefusesWriteMethodsOfThisModule(t *testing.T) {
	pg := "import \"" + modulePath + "/internal/adapters/outbound/postgres\"\n"
	bad := map[string]string{
		// G3: a write method on a value an allowlisted constructor returned.
		"a policy save on a constructed repo": "package x\n" + pg + "func f() { _ = postgres.NewPolicyRepo(pool).Save(ctx, rec, 0) }\n",
		"a trip save on a repo variable":      "package x\n" + pg + "func f() { r := postgres.NewStationTripRepo(pool); _ = r.Save(ctx, trip, 0) }\n",
		"a method value of a write":           "package x\n" + pg + "func f() { save := postgres.NewPolicyRepo(pool).Save; _ = save }\n",
		"a method expression of a write":      "package x\n" + pg + "var _ = (*postgres.StationTripRepo).Save\n",
		"a unit of work through a field":      "package x\nfunc (d deps) f() { _ = d.uow.Do(ctx, fn) }\n",
		"a transaction through a field":       "package x\nfunc (d deps) f() { _, _ = d.pool.Begin(ctx) }\n",
		// G4: raw SQL through the pool.
		"Exec on the pool":              "package x\n" + pg + "func f() { p, _ := postgres.NewPool(ctx, dsn); _, _ = p.Exec(ctx, \"DELETE FROM hints\") }\n",
		"raw SQL through pool.Query":    "package x\n" + pg + "func f() { p, _ := postgres.NewPool(ctx, dsn); _, _ = p.Query(ctx, \"DELETE FROM hints\") }\n",
		"raw SQL through pool.QueryRow": "package x\n" + pg + "func f() { p, _ := postgres.NewPool(ctx, dsn); _ = p.QueryRow(ctx, \"UPDATE hints SET state = 'x' RETURNING 1\") }\n",
		"a pooled connection":           "package x\n" + pg + "func f() { p, _ := postgres.NewPool(ctx, dsn); c, _ := p.Acquire(ctx); _ = c.Conn().PgConn().ExecParams(ctx, \"DELETE FROM hints\", nil, nil, nil, nil) }\n",
		// G5: a raw driver import.
		"a pgx import":          "package x\nimport _ \"github.com/jackc/pgx/v5/pgxpool\"\n",
		"a pgx root import":     "package x\nimport _ \"github.com/jackc/pgx/v5\"\n",
		"a database/sql import": "package x\nimport _ \"database/sql\"\n",
	}
	for _, verb := range agentWriteVerbsWant {
		bad["the write verb "+verb] = "package x\nfunc f(v repo) { _ = v." + verb + " }\n"
	}
	for _, surface := range []string{"mcp", "a2a"} {
		s := hardeningSurface(t, surface)
		var missed []string
		for name, src := range bad {
			if got, err := agentSourceViolations(s, "fixture.go", src); err != nil || len(got) == 0 {
				missed = append(missed, name)
			}
		}
		if len(missed) > 0 {
			t.Errorf("%s: the sensor does not flag %d fixture(s): %s", surface, len(missed), strings.Join(missed, "; "))
		}
	}
}
