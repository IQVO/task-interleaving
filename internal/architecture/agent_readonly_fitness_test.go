// templates/architecture/agent_readonly_fitness_test.go.tmpl — the fleet sensor for READ-ONLY agent
// surfaces (an MCP server or an A2A agent whose charter is read-only: fleet rule agent-authority,
// "a read-only MCP charter stays read-only until that context's own ADR amends it").
//
// Copy VERBATIM to internal/architecture/agent_readonly_fitness_test.go together with
// agent_readonly_methods_fitness_test.go.tmpl (verbatim) and agent_readonly_config_test.go.tmpl (the
// repo's half: which surfaces, which read identifiers, which write adapters). It uses the package's
// modulePath constant (architecture_test.go) and nothing else of the repo, so a later template fix
// is a byte copy. Change nothing here; a repo difference belongs in the config file.
//
// Ported from material-flow-control 2556afc and amr-fleet-control 52797e0 (branches
// feature/*-core), the two sensors hardened after the asrs-control 007bf7b original let five gaps
// through (fleet audit 2026-11-21):
//
//	G1  a qualifier matched by its LOCAL name: `uc "…/usecases"` hid uc.NewService. Here every
//	    qualifier is resolved through the file's imports to its import path (agentQualifiers).
//	G2  an alias or dot import of a guarded package: both are refused outright.
//	G3  a write METHOD on a value an allowlisted constructor returned (postgres.NewRepo(p).Save):
//	    agent_readonly_methods_fitness_test.go refuses every write verb on any value.
//	G4  pgx raw-SQL methods (Query, QueryRow, Exec, Begin, Acquire, ...): refused there too.
//	G5  a raw driver import (jackc/pgx, database/sql): refused there too.
//
// The rules, per source file of each surface's adapter and cmd/ directory:
//   - the only identifiers of a guarded package (agentGuardedPackages) the file may reference are
//     its allowlist (agentAllowedSelectors). Allowlist, not denylist: a new write use case is
//     refused without being named first.
//   - no forbidden import (agentForbiddenImports: Kafka, the outbox, the other write-side
//     adapters, the memory doubles, the REST adapter) and no import of the sibling agent adapter.
//   - a `Reads` interface, when the file declares one, has exactly the surface's read methods.
//   - every configured surface is built: a missing adapter or cmd/ directory, or one with no
//     sources, fails (no vacuous pass).
//
// The *Bites tests build their fixtures from the config, so each rule is proven to fire for THIS
// repo's guarded packages, forbidden imports and read seam. Harness selftest:
// internal/architecture/agent_readonly_template_test.go (warehouse-harness-template).
package architecture

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// agentSurface is one read-only agent adapter and its composition root (paths relative to this
// package, internal/architecture).
type agentSurface struct {
	name, adapterDir, cmdDir string
	reads                    []string // the exact method set of the adapter's Reads seam
	forbidSibling            string   // import-path suffix of the other agent adapter, "" when none
}

func TestAgentSurfacesAreReadOnly(t *testing.T) {
	if len(agentSurfaces) == 0 {
		t.Fatal("agentSurfaces is empty: the sensor checks nothing (configure agent_readonly_config_test.go)")
	}
	for _, s := range agentSurfaces {
		if p := agentSurfaceProblem(s); p != "" {
			t.Fatal(p)
		}
		for _, dir := range []string{s.adapterDir, s.cmdDir} {
			files := agentSourcesUnder(t, dir)
			if len(files) == 0 {
				t.Fatalf("no %s sources under %s; the sensor is looking in the wrong place", s.name, dir)
			}
			for _, path := range files {
				for _, v := range agentFileViolations(t, s, path) {
					t.Errorf("%s %s (the %s surface is read-only: fleet rule agent-authority)", path, v, s.name)
				}
			}
		}
	}
}

// agentSourcesUnder is every non-test .go file under root.
func agentSourcesUnder(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return files
}

func agentFileViolations(t *testing.T, s agentSurface, path string) []string {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	violations, err := agentSourceViolations(s, path, src)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return violations
}

// agentSourceViolations is every rule over one source (a file, or a fixture string).
func agentSourceViolations(s agentSurface, filename string, src any) ([]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), filename, src, 0)
	if err != nil {
		return nil, err
	}
	names, out := agentQualifiers(f)
	out = append(out, agentImportViolations(s, f)...)
	out = append(out, agentSelectorViolations(f, names)...)
	out = append(out, agentWriteMethodViolations(f, names)...)
	return append(out, agentReadsViolations(s, f)...), nil
}

func agentImportViolations(s agentSurface, f *ast.File) []string {
	var out []string
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		for fragment, why := range agentForbiddenImports {
			if strings.Contains(path, fragment) {
				out = append(out, "imports "+path+": "+why)
			}
		}
		if s.forbidSibling != "" && strings.Contains(path, s.forbidSibling) {
			out = append(out, "imports "+path+": the agent adapters are siblings")
		}
	}
	return out
}

// guardedKey is the allowlist key of an import path, "" when the path is not a guarded package.
func guardedKey(path string) string {
	for suffix, key := range agentGuardedPackages {
		if strings.HasSuffix(path, suffix) {
			return key
		}
	}
	return ""
}

// agentQualifiers resolves every local package name of f to its import path (the alias, or the last
// path element) and refuses a dot or aliased import of a guarded package: a dot import hides every
// selector the allowlist reads, and an alias only obscures which package a reader sees (G1, G2).
func agentQualifiers(f *ast.File) (map[string]string, []string) {
	names, out := map[string]string{}, []string(nil)
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		local := path[strings.LastIndex(path, "/")+1:]
		if imp.Name != nil {
			local = imp.Name.Name
		}
		if v := agentAliasViolation(path, local, imp.Name != nil); v != "" {
			out = append(out, v)
		}
		if local != "." && local != "_" {
			names[local] = path
		}
	}
	return names, out
}

func agentAliasViolation(path, local string, named bool) string {
	key := guardedKey(path)
	switch {
	case key == "" || !named || local == "_":
		return ""
	case local == ".":
		return "dot-imports " + path + ": a guarded package must be referenced as " + key + ".<Name>"
	case local != key:
		return "imports " + path + " as " + local + ": a guarded package must keep its name " + key
	}
	return ""
}

func agentSelectorViolations(f *ast.File, names map[string]string) []string {
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Obj != nil { // not a qualifier, or a name declared in this file
			return true
		}
		key := pkg.Name // a qualifier no import binds (a fixture): judged by its name
		if path, imported := names[pkg.Name]; imported {
			key = guardedKey(path)
		}
		if allowed, guarded := agentAllowedSelectors[key]; guarded && !slices.Contains(allowed, sel.Sel.Name) {
			out = append(out, "references "+pkg.Name+"."+sel.Sel.Name+" ("+key+"), which is not on the read-only allowlist")
		}
		return true
	})
	return out
}

func agentReadsViolations(s agentSurface, f *ast.File) []string {
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		spec, ok := n.(*ast.TypeSpec)
		if !ok || spec.Name.Name != "Reads" {
			return true
		}
		iface, ok := spec.Type.(*ast.InterfaceType)
		if !ok {
			return true
		}
		var got []string
		for _, m := range iface.Methods.List {
			for _, name := range m.Names {
				got = append(got, name.Name)
			}
		}
		sort.Strings(got)
		want := slices.Sorted(slices.Values(s.reads))
		if strings.Join(got, ",") != strings.Join(want, ",") {
			out = append(out, "Reads declares "+strings.Join(got, ",")+", want exactly "+strings.Join(want, ","))
		}
		return false
	})
	return out
}

func agentDirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// agentSurfaceProblem is why s cannot be checked: a missing adapter or cmd/ directory.
func agentSurfaceProblem(s agentSurface) string {
	adapter, cmd := agentDirExists(s.adapterDir), agentDirExists(s.cmdDir)
	if !adapter || !cmd {
		return fmt.Sprintf("the %s surface needs both %s (exists=%v) and %s (exists=%v)", s.name, s.adapterDir, adapter, s.cmdDir, cmd)
	}
	return ""
}

func TestEveryAgentSurfaceMustBeBuilt(t *testing.T) {
	absent := filepath.Join(t.TempDir(), "absent")
	for _, s := range agentSurfaces {
		for name, c := range map[string][2]string{
			"both dirs absent": {absent, absent},
			"no cmd/ dir":      {s.adapterDir, absent},
			"no adapter dir":   {absent, s.cmdDir},
		} {
			broken := s
			broken.adapterDir, broken.cmdDir = c[0], c[1]
			if p := agentSurfaceProblem(broken); p == "" || !strings.Contains(p, s.name) {
				t.Errorf("%s surface with %s: problem %q, want a failure naming the surface", s.name, name, p)
			}
		}
	}
}

// agentImportFixturePath turns a forbidden fragment into an import path: a module-relative suffix
// ("/internal/...") is joined to modulePath, anything else is used as it is.
func agentImportFixturePath(fragment string) string {
	if strings.HasPrefix(fragment, "/") {
		return modulePath + fragment
	}
	return fragment
}

// agentBadFixtures is one source per rule, built from the config, that the sensor must flag.
func agentBadFixtures(s agentSurface) map[string]string {
	bad := map[string]string{}
	for suffix, key := range agentGuardedPackages {
		imp := strconv.Quote(modulePath + suffix)
		bad["a write seam of "+key] = "package x\nvar _ = " + key + ".NotOnTheReadOnlyAllowlist\n"
		bad["an aliased write seam of "+key] = "package x\nimport zz " + imp + "\nvar _ = zz.NotOnTheReadOnlyAllowlist\n"
		bad["an aliased import of "+key] = "package x\nimport zz " + imp + "\nvar _ = zz\n"
		bad["a dot import of "+key] = "package x\nimport . " + imp + "\nvar _ = NotOnTheReadOnlyAllowlist\n"
	}
	for fragment := range agentForbiddenImports {
		bad["the forbidden import "+fragment] = "package x\nimport _ " + strconv.Quote(agentImportFixturePath(fragment)) + "\n"
	}
	if s.forbidSibling != "" {
		bad["the sibling adapter"] = "package x\nimport _ " + strconv.Quote(modulePath+s.forbidSibling) + "\n"
	}
	if len(s.reads) > 0 {
		bad["a write method on Reads"] = "package x\ntype Reads interface{ " + strings.Join(s.reads, "(); ") + "(); CancelEverything() }\n"
		bad["a missing read method"] = "package x\ntype Reads interface{}\n"
	}
	return bad
}

// agentGoodFixtures are the read wirings the sensor must accept: every allowlisted identifier through
// its guarded import (bound to its own name), write seams named only in a comment, the Reads seam in
// another order, and a guarded local NAME bound to an unguarded import path.
func agentGoodFixtures(s agentSurface) map[string]string {
	var imports, uses strings.Builder
	for _, suffix := range slices.Sorted(maps.Keys(agentGuardedPackages)) {
		key := agentGuardedPackages[suffix]
		imports.WriteString("import " + key + " " + strconv.Quote(modulePath+suffix) + "\n")
		for _, id := range agentAllowedSelectors[key] {
			uses.WriteString("var _ = " + key + "." + id + "\n")
		}
	}
	wiring := "package x\n" + imports.String() + "// write seams named only in a comment: usecases.NewService, postgres.NewUnitOfWork\n" + uses.String()
	if len(s.reads) > 0 {
		reads := slices.Clone(s.reads)
		slices.Reverse(reads)
		wiring += "type Reads interface{ " + strings.Join(reads, "(); ") + "() }\n"
	}
	good := map[string]string{"the read wiring": wiring}
	for _, key := range agentGuardedPackages {
		good["a local name "+key+" bound to another path"] = "package x\nimport " + key + " \"example.com/elsewhere/" + key + "\"\nvar _ = " + key + ".NotOnTheReadOnlyAllowlist\n"
	}
	return good
}

func TestAgentReadOnlySensorBites(t *testing.T) {
	for _, s := range agentSurfaces {
		for name, src := range agentBadFixtures(s) {
			if got, err := agentSourceViolations(s, "fixture.go", src); err != nil || len(got) == 0 {
				t.Errorf("%s surface: the sensor does not flag %s (violations=%v, err=%v)", s.name, name, got, err)
			}
		}
		for name, src := range agentGoodFixtures(s) {
			if got, err := agentSourceViolations(s, "fixture.go", src); err != nil || len(got) != 0 {
				t.Errorf("%s surface: false positive on %s: %v (err=%v)", s.name, name, got, err)
			}
		}
	}
}
