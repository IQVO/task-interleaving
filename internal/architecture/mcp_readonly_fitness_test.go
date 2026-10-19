package architecture

// Read-only MCP sensor (docs/adr/0007, .claude/rules/fleet/no-auth-and-mcp.md).
// The MCP server is an ADDITIVE, READ-ONLY inbound adapter: it can read
// policies, hints and stations and can never change them or emit an event.
// "Read-only" is pinned structurally, not by review:
//
//   - the adapter's Deps struct holds only the four read use cases;
//   - the adapter imports no write-capable seam (ports, the outbox, an event
//     sink, a unit of work);
//   - cmd/mcp wires only those four use cases and imports neither the events
//     sink, the outbox relay, nor any Kafka adapter, consumer or worker.
//
// TestMCPReadOnlySensorBites proves each detector flags what it should.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// readUseCases is the whole set of use cases the MCP surface may hold or wire.
var readUseCases = map[string]bool{"GetPolicy": true, "ListHints": true, "GetHint": true, "GetStation": true}

// mcpAllowedUsecaseRefs are the non-use-case names of package usecases the MCP
// sources may refer to: read inputs and outputs and the typed errors a read
// can raise. Everything else in package usecases is a write path (or a seam
// to one) and is refused.
var mcpAllowedUsecaseRefs = map[string]bool{
	"GetPolicy": true, "ListHints": true, "GetHint": true, "GetStation": true,
	"ListHintsQuery": true, "HintPage": true,
	"ErrInvalidCursor": true, "ErrInvalidLimit": true, "ErrInvalidState": true,
}

// mcpForbiddenImportParts are import-path fragments no MCP source may carry:
// the write-capable seams and every moving part of the write side.
var mcpForbiddenImportParts = []string{
	"/internal/application/ports",
	"/internal/application/outbox",
	"/internal/adapters/outbound/events",
	"/internal/adapters/outbound/outbox",
	"/internal/adapters/outbound/kafka",
	"/internal/adapters/outbound/memory",
	"/internal/adapters/inbound/kafka",
	"/internal/adapters/inbound/expiry",
	"/internal/adapters/inbound/http",
	"/internal/adapters/kafka",
	"segmentio/kafka-go",
}

// mcpImportViolations returns one finding per forbidden import of one source.
func mcpImportViolations(filename string, src any) ([]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), filename, src, parser.ImportsOnly)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			return nil, err
		}
		for _, part := range mcpForbiddenImportParts {
			if strings.Contains(path, part) {
				out = append(out, fmt.Sprintf("%s imports %s", filename, path))
			}
		}
	}
	return out, nil
}

// depsFieldViolations returns every field of the struct named Deps in src
// whose type is not *usecases.<read use case>.
func depsFieldViolations(filename string, src any) ([]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), filename, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		spec, ok := n.(*ast.TypeSpec)
		if !ok || spec.Name.Name != "Deps" {
			return true
		}
		st, ok := spec.Type.(*ast.StructType)
		if !ok {
			return true
		}
		for _, field := range st.Fields.List {
			if name, ok := usecaseName(field.Type); ok && readUseCases[name] {
				continue
			}
			names := make([]string, 0, len(field.Names))
			for _, id := range field.Names {
				names = append(names, id.Name)
			}
			out = append(out, fmt.Sprintf("%s: Deps field %s is not a read use case", filename, strings.Join(names, ",")))
		}
		return false
	})
	return out, nil
}

// usecaseName returns N for the type expression *usecases.N.
func usecaseName(e ast.Expr) (string, bool) {
	star, ok := e.(*ast.StarExpr)
	if !ok {
		return "", false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "usecases" {
		return "", false
	}
	return sel.Sel.Name, true
}

// wiredUseCaseViolations returns every usecases.<Name> a source refers to that
// is not in mcpAllowedUsecaseRefs (the MCP sources must not even name a write
// use case).
func wiredUseCaseViolations(filename string, src any) ([]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), filename, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "usecases" && !mcpAllowedUsecaseRefs[sel.Sel.Name] {
			out = append(out, fmt.Sprintf("%s refers to usecases.%s, which is not a read use case or a read input/output", filename, sel.Sel.Name))
		}
		return true
	})
	return out, nil
}

func mcpReadOnlyFiles(t *testing.T) (adapter, command []string) {
	t.Helper()
	adapter = goFilesUnder(t, "../adapters/inbound/mcp", false)
	command = goFilesUnder(t, filepath.Join("..", "..", "cmd", "mcp"), false)
	if len(adapter) == 0 || len(command) == 0 {
		t.Fatalf("no non-test Go file under the MCP adapter (%d) or cmd/mcp (%d): the sensor would pass vacuously", len(adapter), len(command))
	}
	sort.Strings(adapter)
	sort.Strings(command)
	return adapter, command
}

// TestMCPServerIsReadOnly: the MCP adapter and cmd/mcp can reach no write
// path.
func TestMCPServerIsReadOnly(t *testing.T) {
	adapter, command := mcpReadOnlyFiles(t)
	for _, path := range append(append([]string{}, adapter...), command...) {
		found, err := mcpImportViolations(path, nil)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, v := range found {
			t.Errorf("READ-ONLY MCP VIOLATION: %s -- the MCP surface may read through the use cases only (docs/adr/0007)", v)
		}
	}
	for _, path := range adapter {
		found, err := depsFieldViolations(path, nil)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, v := range found {
			t.Errorf("READ-ONLY MCP VIOLATION: %s -- the MCP adapter may hold read use cases only (docs/adr/0007)", v)
		}
	}
	for _, path := range append(append([]string{}, adapter...), command...) {
		found, err := wiredUseCaseViolations(path, nil)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, v := range found {
			t.Errorf("READ-ONLY MCP VIOLATION: %s -- wire only the read use cases (docs/adr/0007)", v)
		}
	}
}

func TestMCPReadOnlySensorBites(t *testing.T) {
	imports := []struct {
		name string
		src  string
		want int
	}{
		{"ports is flagged", "package x\nimport \"github.com/claudioed/task-interleaving/internal/application/ports\"", 1},
		{"the event sink is flagged", "package x\nimport \"github.com/claudioed/task-interleaving/internal/adapters/outbound/events\"", 1},
		{"the outbox relay is flagged", "package x\nimport \"github.com/claudioed/task-interleaving/internal/adapters/outbound/outbox\"", 1},
		{"a kafka consumer is flagged", "package x\nimport \"github.com/claudioed/task-interleaving/internal/adapters/inbound/kafka\"", 1},
		{"kafka-go is flagged", "package x\nimport \"github.com/segmentio/kafka-go\"", 1},
		{"the memory doubles are flagged", "package x\nimport \"github.com/claudioed/task-interleaving/internal/adapters/outbound/memory\"", 1},
		{"usecases and repository pass", "package x\nimport (\n\"github.com/claudioed/task-interleaving/internal/application/usecases\"\n\"github.com/claudioed/task-interleaving/internal/application/repository\"\n)", 0},
		{"the postgres adapter passes", "package x\nimport \"github.com/claudioed/task-interleaving/internal/adapters/outbound/postgres\"", 0},
	}
	for _, tc := range imports {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mcpImportViolations("src.go", tc.src)
			if err != nil || len(got) != tc.want {
				t.Fatalf("findings = %v (err %v), want %d", got, err, tc.want)
			}
		})
	}

	deps := []struct {
		name string
		src  string
		want int
	}{
		{"a write use case field is flagged", "package x\ntype Deps struct{ Declare *usecases.DeclarePolicy }", 1},
		{"a unit of work field is flagged", "package x\ntype Deps struct{ UoW ports.UnitOfWork }", 1},
		{"a non-pointer read use case is flagged", "package x\ntype Deps struct{ Get usecases.GetPolicy }", 1},
		{"the four read use cases pass", "package x\ntype Deps struct{\nA *usecases.GetPolicy\nB *usecases.ListHints\nC *usecases.GetHint\nD *usecases.GetStation\n}", 0},
		{"other structs are not Deps", "package x\ntype Other struct{ W *usecases.DeclarePolicy }", 0},
	}
	for _, tc := range deps {
		t.Run(tc.name, func(t *testing.T) {
			got, err := depsFieldViolations("src.go", tc.src)
			if err != nil || len(got) != tc.want {
				t.Fatalf("findings = %v (err %v), want %d", got, err, tc.want)
			}
		})
	}

	wired := []struct {
		name string
		src  string
		want int
	}{
		{"constructing a write use case is flagged", "package main\nvar _ = &usecases.DeclarePolicy{}", 1},
		{"naming the expiry sweeper is flagged", "package main\nvar _ *usecases.ExpireHints", 1},
		{"read use cases pass", "package main\nvar _ = &usecases.GetHint{}\nvar _ = &usecases.ListHints{}", 0},
	}
	for _, tc := range wired {
		t.Run(tc.name, func(t *testing.T) {
			got, err := wiredUseCaseViolations("src.go", tc.src)
			if err != nil || len(got) != tc.want {
				t.Fatalf("findings = %v (err %v), want %d", got, err, tc.want)
			}
		})
	}
}
