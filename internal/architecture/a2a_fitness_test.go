package architecture

// A2A inbound adapter sensors (docs/adr/0009). The A2A server is an ADDITIVE,
// READ-ONLY third driving adapter, exactly like the MCP server it inherits its
// charter from (docs/adr/0007): the write gate of the A2A rollout allows a
// skill to change state only if the MCP surface already offers that write, and
// it offers none. Pinned structurally, reusing the MCP sensor's detectors:
//
//   - the adapter depends only on application and domain, and nothing depends
//     on it (TestA2AAdapterDependencyRule);
//   - the adapter's Deps holds read use cases only, no source file of the
//     adapter or cmd/a2a imports a write-capable seam or names a write use case
//     (TestA2AServerIsReadOnly);
//   - this context has no OUTBOUND A2A client: it never delegates to a sibling
//     (CLAUDE.md rule 6; TestNoOutboundA2AClient, proven by
//     TestOutboundA2AClientSensorBites).

import (
	"fmt"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	archgo "github.com/arch-go/arch-go/api"
	"github.com/arch-go/arch-go/api/configuration"
)

func TestA2AAdapterDependencyRule(t *testing.T) {
	moduleInfo := configuration.Load(modulePath)

	t.Run("a2a adapter depends only on application and domain", func(t *testing.T) {
		result := archgo.CheckArchitecture(moduleInfo, configuration.Config{
			DependenciesRules: []*configuration.DependenciesRule{
				{
					Package: "**.internal.adapters.inbound.a2a.**",
					ShouldOnlyDependsOn: &configuration.Dependencies{
						Internal: []string{
							"**.internal.adapters.inbound.a2a.**",
							"**.internal.application.**",
							"**.internal.domain.**",
						},
					},
				},
			},
		})
		assertArchGoPasses(t, result)
	})

	t.Run("nothing else depends on the a2a adapter", func(t *testing.T) {
		result := archgo.CheckArchitecture(moduleInfo, configuration.Config{
			DependenciesRules: []*configuration.DependenciesRule{
				{
					Package: "**.internal.**",
					ShouldNotDependsOn: &configuration.Dependencies{
						Internal: []string{"**.internal.adapters.inbound.a2a.**"},
					},
				},
			},
		})
		assertArchGoPasses(t, result)
	})
}

func a2aReadOnlyFiles(t *testing.T) (adapter, command []string) {
	t.Helper()
	adapter = goFilesUnder(t, "../adapters/inbound/a2a", false)
	command = goFilesUnder(t, filepath.Join("..", "..", "cmd", "a2a"), false)
	if len(adapter) == 0 || len(command) == 0 {
		t.Fatalf("no non-test Go file under the A2A adapter (%d) or cmd/a2a (%d): the sensor would pass vacuously", len(adapter), len(command))
	}
	sort.Strings(adapter)
	sort.Strings(command)
	return adapter, command
}

// TestA2AServerIsReadOnly: the A2A adapter and cmd/a2a can reach no write
// path.
func TestA2AServerIsReadOnly(t *testing.T) {
	adapter, command := a2aReadOnlyFiles(t)
	all := append(append([]string{}, adapter...), command...)
	for _, path := range all {
		found, err := mcpImportViolations(path, nil)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, v := range found {
			t.Errorf("READ-ONLY A2A VIOLATION: %s -- the A2A surface may read through the use cases only (docs/adr/0009)", v)
		}
	}
	for _, path := range adapter {
		found, err := depsFieldViolations(path, nil)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, v := range found {
			t.Errorf("READ-ONLY A2A VIOLATION: %s -- the A2A adapter may hold read use cases only (docs/adr/0009)", v)
		}
	}
	for _, path := range all {
		found, err := wiredUseCaseViolations(path, nil)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, v := range found {
			t.Errorf("READ-ONLY A2A VIOLATION: %s -- wire only the read use cases (docs/adr/0009)", v)
		}
	}
}

// a2aClientImportSuffix is the SDK package of an outbound A2A client.
const a2aClientImportSuffix = "a2a-go/v2/a2aclient"

// outboundA2AClientImports returns one finding per import of the SDK's client
// package (or a package below it) in one source.
func outboundA2AClientImports(filename string, src any) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.ImportsOnly)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			return nil, err
		}
		if strings.HasSuffix(path, a2aClientImportSuffix) || strings.Contains(path, a2aClientImportSuffix+"/") {
			out = append(out, fmt.Sprintf("%s imports %s", fset.Position(imp.Pos()), path))
		}
	}
	return out, nil
}

// TestNoOutboundA2AClient: this context is delegated TO and never delegates
// (CLAUDE.md rule 6: no live lookups of sibling contexts). No non-test file
// under cmd/ or internal/ may import the A2A client.
func TestNoOutboundA2AClient(t *testing.T) {
	files := append(goFilesUnder(t, "..", false), goFilesUnder(t, filepath.Join("..", "..", "cmd"), false)...)
	if len(files) == 0 {
		t.Fatal("no Go file found: the sensor would pass vacuously")
	}
	for _, path := range files {
		found, err := outboundA2AClientImports(path, nil)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, v := range found {
			t.Errorf("OUTBOUND A2A CLIENT: %s -- this context has no outbound client to a sibling (CLAUDE.md rule 6, docs/adr/0009)", v)
		}
	}
}

func TestOutboundA2AClientSensorBites(t *testing.T) {
	for name, tc := range map[string]struct {
		src  string
		want int
	}{
		"the SDK client is flagged":     {"package x\nimport \"github.com/a2aproject/a2a-go/v2/a2aclient\"", 1},
		"an aliased client is flagged":  {"package x\nimport c \"github.com/a2aproject/a2a-go/v2/a2aclient\"", 1},
		"a nested client package":       {"package x\nimport \"github.com/a2aproject/a2a-go/v2/a2aclient/jsonrpc\"", 1},
		"the server SDK package passes": {"package x\nimport \"github.com/a2aproject/a2a-go/v2/a2asrv\"", 0},
		"the core SDK package passes":   {"package x\nimport \"github.com/a2aproject/a2a-go/v2/a2a\"", 0},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := outboundA2AClientImports("src.go", tc.src)
			if err != nil || len(got) != tc.want {
				t.Fatalf("findings = %v (err %v), want %d", got, err, tc.want)
			}
		})
	}
}
