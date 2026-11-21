// templates/architecture/agent_readonly_methods_fitness_test.go.tmpl — the second half of the
// read-only agent sensor (gaps G3-G5 of agent_readonly_fitness_test.go.tmpl). Copy VERBATIM to
// internal/architecture/agent_readonly_methods_fitness_test.go; a repo's own write use cases go in
// agentRepoWriteVerbs (agent_readonly_config_test.go), never here.
//
// Ported from material-flow-control 2556afc and amr-fleet-control 52797e0
// (internal/architecture/agent_readonly_methods_fitness_test.go).
package architecture

import (
	"go/ast"
	"go/types"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// agentFleetWriteVerbs are the method names agent sources may not select on ANY value: the write
// methods of the postgres repos and the unit of work (Save, Insert, Update, Delete, Lock, Purge,
// Append, Enqueue) and of the pgx pool and its transactions (Exec, Begin, BeginTx, CopyFrom,
// SendBatch), plus the pool's raw-SQL entry points (Query, QueryRow, Acquire, AcquireFunc,
// AcquireAllIdle, Conn, PgConn, ExecParams, Hijack). A raw pool.Query runs ANY SQL in autocommit,
// outside the BEGIN READ ONLY a read-only pool opens, so it is a write seam. The only legit pool
// methods in agent sources are Close and Ping; the reader's reads go through the postgres adapter.
//
// A verb list, not a type check: the sensor parses (fixtures need no module to compile against), so
// a selector is judged by its name. A package-qualified FUNCTION is not a method and is judged by
// the allowlist instead (slices.Insert, slices.Delete, time.Tick stay allowed). Anything else with
// one of these names is refused even when it is not a database write (a sync.Mutex Lock): an agent
// source has no business with either, and a real need adds its exact expression to
// agentWriteVerbAllowances with the reason.
var agentFleetWriteVerbs = []string{"Save", "Insert", "Update", "Delete", "Lock", "Purge", "Append", "Enqueue",
	"Exec", "Begin", "BeginTx", "CopyFrom", "SendBatch",
	"Query", "QueryRow", "Acquire", "AcquireFunc", "AcquireAllIdle", "Conn", "PgConn", "ExecParams", "Hijack"}

// agentRawDriverImports are refused outright: the read pool comes from the postgres adapter's
// constructor only, so a raw driver import can only mean a second, unguarded way to the database.
var agentRawDriverImports = []string{"github.com/jackc/pgx", "database/sql"}

// agentWriteVerbs is the fleet verbs plus the service's own (agentRepoWriteVerbs).
func agentWriteVerbs() []string {
	return append(slices.Clone(agentFleetWriteVerbs), agentRepoWriteVerbs...)
}

// agentWriteMethodViolations refuses a write verb selected on anything but an imported package, and
// a raw driver import. names resolves each local package name of f (agentQualifiers).
func agentWriteMethodViolations(f *ast.File, names map[string]string) []string {
	var out []string
	for _, imp := range f.Imports {
		if path, err := strconv.Unquote(imp.Path.Value); err == nil {
			for _, raw := range agentRawDriverImports {
				if path == raw || strings.HasPrefix(path, raw+"/") {
					out = append(out, "imports "+path+": a raw database driver (the read pool comes from the postgres adapter only)")
				}
			}
		}
	}
	verbs := agentWriteVerbs()
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok || !slices.Contains(verbs, sel.Sel.Name) {
			return true
		}
		if pkg, isIdent := sel.X.(*ast.Ident); isIdent && pkg.Obj == nil && names[pkg.Name] != "" {
			return true // a package-qualified function: the allowlist judges it
		}
		expr := types.ExprString(sel)
		if _, allowed := agentWriteVerbAllowances[expr]; !allowed {
			out = append(out, "selects "+expr+": "+sel.Sel.Name+" is a write method or raw SQL (the surface reads only)")
		}
		return true
	})
	return out
}

// The allowlist judges package-qualified identifiers only, so without this half a write METHOD on a
// value an allowlisted constructor returned (postgres.NewRepo(pool).Save, pool.Exec), raw SQL through
// the read pool and a raw driver import got through (asrs-control 007bf7b).
func TestAgentReadOnlySensorRefusesWriteMethodsAndRawDrivers(t *testing.T) {
	pg := "import \"" + modulePath + "/internal/adapters/outbound/postgres\"\n"
	bad := map[string]string{
		"a write method on a constructed repo": "package x\n" + pg + "func f() { _ = postgres.NewRepo(nil).Save(ctx, nil) }\n",
		"a write method on a repo variable":    "package x\n" + pg + "func f() { r := postgres.NewRepo(nil); _ = r.Insert(ctx, nil) }\n",
		"a method value of a write":            "package x\n" + pg + "func f() { save := postgres.NewRepo(nil).Save; _ = save }\n",
		"a write method through a field":       "package x\nfunc (d deps) f() { _, _ = d.pool.Begin(ctx) }\n",
		"a method expression of a write":       "package x\n" + pg + "var _ = (*postgres.Repo).Save\n",
		"raw SQL through pool.Exec":            "package x\nfunc f(p pool) { _, _ = p.Exec(ctx, \"DELETE FROM t\") }\n",
		"raw SQL through pool.Query":           "package x\nfunc f(p pool) { _, _ = p.Query(ctx, \"DELETE FROM t\") }\n",
		"raw SQL through pool.QueryRow":        "package x\nfunc f(p pool) { _ = p.QueryRow(ctx, \"UPDATE t SET x = 1 RETURNING 1\") }\n",
		"a pooled connection":                  "package x\nfunc f(p pool) { c, _ := p.Acquire(ctx); _ = c.Conn().PgConn().ExecParams(ctx, \"DELETE FROM t\", nil, nil, nil, nil) }\n",
	}
	for _, raw := range []string{"github.com/jackc/pgx/v5", "github.com/jackc/pgx/v5/pgxpool", "database/sql"} {
		bad["the driver import "+raw] = "package x\nimport _ \"" + raw + "\"\n"
	}
	for _, verb := range agentWriteVerbs() {
		if _, allowed := agentWriteVerbAllowances["v."+verb]; !allowed {
			bad["the write verb "+verb] = "package x\nfunc f(v repo) { _ = v." + verb + " }\n"
		}
	}
	good := "package x\nimport (\n\"slices\"\n\"time\"\n)\n" +
		"func f(pool pool, xs []int) { defer pool.Close(); _ = pool.Ping(ctx)\n" +
		"s := slices.Insert(xs, 0, 1); _ = slices.Delete(s, 0, 1); _ = time.Tick }\n"
	for _, s := range agentSurfaces {
		for name, src := range bad {
			if got, err := agentSourceViolations(s, "fixture.go", src); err != nil || len(got) == 0 {
				t.Errorf("%s surface: the sensor does not flag %s (violations=%v, err=%v)", s.name, name, got, err)
			}
		}
		if got, err := agentSourceViolations(s, "fixture.go", good); err != nil || len(got) != 0 {
			t.Errorf("%s surface: false positive on pool.Close/Ping and package-qualified helpers: %v (err=%v)", s.name, got, err)
		}
	}
}
