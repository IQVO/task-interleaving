// Package mcp is the inbound Model Context Protocol adapter: it exposes this
// bounded context to the AI ecosystem as a second driving adapter over the
// same application-layer read use cases the REST adapter uses. It is built on
// the official MCP Go SDK and served over Streamable HTTP only.
//
// Per docs/adr/0007-mcp-server-adoption.md the surface is READ-ONLY: every
// tool reads interleaving data (get_site_policy, list_hints, get_hint,
// get_station); declaring a policy and the hint lifecycle stay on REST and on
// the event consumers, and governance_test.go fails the build on a write-verb
// tool name. This package depends inward on the application layer and the
// domain only, never on an outbound adapter or the REST adapter, and nothing
// else may depend on it (internal/architecture's TestMCPAdapterDependencyRule;
// TestMCPServerIsReadOnly pins that no write use case is reachable). The
// composition root (cmd/mcp) wires concrete repositories into the use cases.
// There is no auth of any kind (fleet-wide revert 2026-09-11;
// TestNoAuthMiddlewareReintroduced).
package mcp

import (
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// serverName and serverVersion identify this server in the MCP initialize
// handshake.
const (
	serverName    = "task-interleaving-mcp"
	serverVersion = "1.0.0"
)

// instructions is what an MCP client is told about this server at initialize.
const instructions = "Task interleaving of the warehouse (read-only, advisory): read a site's interleaving policy with " +
	"get_site_policy (detour limit, candidate cap, hint TTL, eligible task types, vertical weight, version); page " +
	"through the history of interleaving hints, newest first, with list_hints (optionally by station, site and state " +
	"Active, Consumed, Expired or Superseded) or read one with get_hint (its ranked candidate tasks and how it ended); " +
	"and see where a station stands and which hint it currently holds with get_station. A hint is a suggestion for " +
	"which open task to take next, never an assignment. Nothing here changes data: declaring a policy is a REST " +
	"operation of task-interleaving, and hints are issued and retired by the service itself."

// NewServer builds the MCP server for this bounded context with every tool
// registered.
func NewServer(deps Deps) *mcp.Server {
	server := mcp.NewServer(
		&mcp.Implementation{Name: serverName, Version: serverVersion},
		&mcp.ServerOptions{Instructions: instructions},
	)
	deps.registerTools(server)
	return server
}

// Handler returns the Streamable HTTP handler for the MCP server.
func Handler(server *mcp.Server) http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
}
