package architecture

// TEMPORARY (deleted by the commit that adopts the hardened sensor): exposes
// the OLD mcp_readonly detectors (mcpImportViolations, depsFieldViolations,
// wiredUseCaseViolations) under the hardened sensor's entry point, so
// agent_readonly_hardening_test.go runs against the old sensor and its RED is
// an assertion, not a compile error.

type agentSurface struct {
	name, adapterDir, cmdDir string
}

var agentSurfaces = []agentSurface{
	{name: "mcp", adapterDir: "../adapters/inbound/mcp", cmdDir: "../../cmd/mcp"},
	{name: "a2a", adapterDir: "../adapters/inbound/a2a", cmdDir: "../../cmd/a2a"},
}

func agentSourceViolations(_ agentSurface, filename string, src any) ([]string, error) {
	var out []string
	for _, detect := range []func(string, any) ([]string, error){mcpImportViolations, depsFieldViolations, wiredUseCaseViolations} {
		found, err := detect(filename, src)
		if err != nil {
			return nil, err
		}
		out = append(out, found...)
	}
	return out, nil
}
