// Package a2a is the inbound Agent2Agent adapter: it exposes this bounded
// context to the fleet's agent ecosystem as a third driving adapter over the
// same application-layer READ use cases the REST and MCP adapters use. It is
// built on the official A2A Go SDK (a2aproject/a2a-go) and speaks JSON-RPC 2.0
// over HTTP, with the Agent Card served at the well-known path.
//
// Per docs/adr/0009-a2a-inbound-adapter.md, A2A is the DELEGATION channel
// between bounded contexts: an agent client sends a goal-shaped skill request
// as a structured message part and this agent answers from its own state
// through its own use cases. It is not a data-sync channel: facts between
// contexts still travel exclusively as CloudEvents on Kafka.
//
// The surface is READ-ONLY, exactly like the MCP surface (docs/adr/0007): the
// write gate of the A2A rollout says a skill may change state only if the
// context's MCP surface already offers that write, and it offers none. Deps
// holds read use cases and nothing that can write; this package imports no
// ports, outbox, event sink or Kafka package (internal/architecture's
// TestA2AServerIsReadOnly). Like the MCP adapter it depends inward on the
// application layer and the domain only, never on an outbound adapter, and
// nothing else may depend on it. The composition root (cmd/a2a) wires concrete
// repositories into the use cases. Domain structs never leak across the
// message boundary. There is no auth of any kind (fleet-wide revert
// 2026-09-11; TestNoAuthMiddlewareReintroduced).
package a2a

import (
	"context"
	"iter"
	"net/http"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/claudioed/task-interleaving/internal/application/usecases"
)

// Config carries the deployment-specific identity of this agent: the absolute
// URL clients use to reach its JSON-RPC endpoint (advertised on the Agent Card)
// and the card version. It is supplied by the composition root from
// A2A_AGENT_URL; the card MUST advertise a URL the fleet's agent clients can
// actually dial (in-cluster: http://<release>-a2a.<ns>.svc.cluster.local:8093/).
type Config struct {
	AgentURL string
	Version  string
}

// Deps is everything the A2A skills need, injected by the composition root.
// It carries the SAME read use cases the REST and MCP adapters use (GET
// /sites/{siteId}/policy, GET /hints and GET /stations/{stationId}); the
// adapter never constructs an outbound adapter itself and holds no write use
// case at all (docs/adr/0009). POST /recommendations is a simulation and stays
// REST-only.
type Deps struct {
	GetPolicy  *usecases.GetPolicy
	ListHints  *usecases.ListHints
	GetStation *usecases.GetStation
}

// Card builds this agent's Agent Card (docs/adr/0009). The surface is two
// curated, goal-shaped READ skills derived from the MCP tools (not one skill
// per endpoint): station_hint_status answers "what should this station take
// next" and site_interleaving_overview answers "how is interleaving set up and
// behaving at this site". The card declares no security schemes on purpose:
// this surface is unauthenticated by the same fleet-wide decision as REST and
// MCP; schemes are added here, not in a redesign, if that decision is ever
// reversed.
func Card(cfg Config) *a2a.AgentCard {
	return &a2a.AgentCard{
		Name:        "task-interleaving",
		Description: "Task Interleaving agent for the Warehouse Execution System (read-only, advisory): tells which interleaving hint a station currently holds and which open tasks it ranks next by travel detour, and gives a site's interleaving overview (its declared policy and how its most recent hints ended). A hint is a suggestion for which open task to take next, never an assignment. It changes nothing: declaring a policy is a REST operation of task-interleaving, and hints are issued and retired by the service itself.",
		Version:     cfg.Version,
		Provider: &a2a.AgentProvider{
			Org: "IQVO",
			URL: "https://github.com/IQVO/task-interleaving",
		},
		SupportedInterfaces: []*a2a.AgentInterface{
			a2a.NewAgentInterface(cfg.AgentURL, a2a.TransportProtocolJSONRPC),
		},
		DefaultInputModes:  []string{"application/json", "text/plain"},
		DefaultOutputModes: []string{"application/json", "text/plain"},
		Capabilities:       a2a.AgentCapabilities{Streaming: false},
		Skills: []a2a.AgentSkill{
			{
				ID:          skillStationHintStatus,
				Name:        "Interleaving hint status of a station",
				Description: "Read where one station stands and what it should take next: its site, its position (location code and the point in metres when the geometry is known), the last task it completed and when, and the hint it currently holds, if any, with its ranked candidate tasks (rank, taskId, taskType, location, detourM). Advisory only: a hint never assigns or claims a task. A station that never recorded a completion is answered as station-not-found (404).",
				Tags:        []string{"interleaving", "station", "hint", "read"},
				InputModes:  []string{"application/json"},
				Examples: []string{
					`{"skill":"station_hint_status","arguments":{"stationId":"STN-07"}}`,
				},
			},
			{
				ID:          skillSiteInterleavingOverview,
				Name:        "Interleaving overview of a site",
				Description: "Read how interleaving is set up and behaving at one site: the policy the site declared (maxDetourM, maxCandidates, hintTtlSeconds, eligibleTaskTypes, verticalWeight, version, declaredAt) and its most recent hints, newest first, with how each ended (Active, Consumed, Expired or Superseded) and a count per state. limit (optional, 1..20, default 5) sizes the recent-hint window. A site that never declared a policy is inert (no hint is ever issued for it) and is answered as policy-not-found (404).",
				Tags:        []string{"interleaving", "site", "policy", "read"},
				InputModes:  []string{"application/json"},
				Examples: []string{
					`{"skill":"site_interleaving_overview","arguments":{"siteId":"SITE-1"}}`,
					`{"skill":"site_interleaving_overview","arguments":{"siteId":"SITE-1","limit":10}}`,
				},
			},
		},
	}
}

// NewExecutor returns the AgentExecutor that maps an incoming message to a
// skill and answers with a single agent Message (a message-only agent in A2A
// terms): both curated skills are request/response reads, so there is no
// long-running Task lifecycle to track. The response carries the caller's
// contextId (and taskId when the client referenced one) so a delegating saga
// can group its interactions.
func NewExecutor(deps Deps) a2asrv.AgentExecutor {
	return a2asrv.AgentExecutorFunc(func(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			yield(answer(ctx, deps, ec), nil)
		}
	})
}

// Handler wires the executor into the transport-agnostic A2A request handler
// and wraps it in the JSON-RPC 2.0 HTTP handler: the only transport this agent
// ships (docs/adr/0009), mirroring ADR 0007's "Streamable HTTP only"
// minimalism for MCP.
func Handler(executor a2asrv.AgentExecutor) http.Handler {
	return a2asrv.NewJSONRPCHandler(a2asrv.NewHandler(executor))
}
