// The repo's half of the read-only agent sensor, from warehouse-harness-template develop ea62687
// templates/architecture/agent_readonly_config_test.go.tmpl (agent_readonly_fitness_test.go and
// agent_readonly_methods_fitness_test.go are verbatim copies of the other two templates).
//
// Both agent surfaces of this service are READ-ONLY by charter: the MCP server (docs/adr/0007,
// docs/adr/0011: every tool is L1) and the A2A agent (docs/adr/0009). They read policies, hints
// and stations through the four read use cases and can never change them or emit an event.
// Replaces the local-name mcp_readonly sensor (fleet audit 2026-11-21, defect 2); the module's
// bite fixtures are agent_readonly_hardening_test.go.
//
// Allowlist a READ identifier only (a read use case, its input/output, a typed read error, a read
// repository constructor, the migrations runner and the pool); never a write use case, the unit of
// work, the outbox, the location book, the processed-event claim or the idempotency store.
package architecture

// agentSurfaces is every read-only agent surface of the service.
var agentSurfaces = []agentSurface{
	{name: "mcp", adapterDir: "../adapters/inbound/mcp", cmdDir: "../../cmd/mcp", reads: agentReads, forbidSibling: "/internal/adapters/inbound/a2a"},
	{name: "a2a", adapterDir: "../adapters/inbound/a2a", cmdDir: "../../cmd/a2a", reads: agentReads, forbidSibling: "/internal/adapters/inbound/mcp"},
}

// agentReads is empty: neither adapter declares a `Reads` seam. Each is built over a Deps struct of
// read use-case pointers, and the usecases allowlist below is what keeps a write use case out of it.
var agentReads = []string{}

// agentGuardedPackages maps the import-path SUFFIX of each guarded package to its allowlist key.
var agentGuardedPackages = map[string]string{
	"/internal/application/usecases":       "usecases",
	"/internal/application/ports":          "ports",
	"/internal/adapters/outbound/postgres": "postgres",
}

// agentAllowedSelectors maps an allowlist key to the identifiers agent sources may reference through
// it: the four read use cases with the list-hints query, page and typed read errors; the read
// repositories, the migrations runner and the pool of cmd/mcp and cmd/a2a. No port at all.
var agentAllowedSelectors = map[string][]string{
	"usecases": {"GetPolicy", "ListHints", "GetHint", "GetStation", "ListHintsQuery", "HintPage",
		"ErrInvalidCursor", "ErrInvalidLimit", "ErrInvalidState"},
	"ports":    {},
	"postgres": {"RunMigrations", "NewPool", "NewPolicyRepo", "NewStationTripRepo"},
}

// agentForbiddenImports are import-path fragments no agent source may use, and why. The fleet
// entries plus this service's own write side: the outbox port package, the event sink, the
// CloudEvents codec and the hint-expiry worker.
var agentForbiddenImports = map[string]string{
	"a2a-go/v2/a2aclient":                "an outbound A2A client (the agent only answers)",
	"github.com/segmentio/kafka-go":      "Kafka: a read-only surface never dials Kafka",
	"/internal/adapters/outbound/kafka":  "Kafka: a read-only surface publishes no fact",
	"/internal/adapters/inbound/kafka":   "the fact consumers: only cmd/api consumes",
	"/internal/adapters/outbound/outbox": "the outbox relay: a read-only surface never publishes",
	"/internal/adapters/outbound/memory": "the in-memory test doubles (real Postgres only at runtime)",
	"/internal/adapters/inbound/http":    "the REST adapter (the inbound adapters are siblings)",
	"/internal/application/outbox":       "the outbox records: a read-only surface enqueues no fact",
	"/internal/adapters/outbound/events": "the event sink: a read-only surface emits no event",
	"/internal/adapters/kafka":           "the CloudEvents codec: a read-only surface encodes no event",
	"/internal/adapters/inbound/expiry":  "the hint-expiry worker: only cmd/api retires hints",
}

// agentRepoWriteVerbs are this service's write METHODS an agent source could reach on a value: the
// location book (SaveZone, SaveLocation, SaveGeometry, DecommissionLocation), the processed-event
// claim (Claim), the unit of work (Do), the outbox relay (Drain) and the event sink
// (PublishPolicyDeclared, PublishTripEvents). Repo Save is a fleet verb already. Handle is not
// listed: the read use cases answer through Handle too, and the usecases allowlist already keeps
// every write use case out.
var agentRepoWriteVerbs = []string{"SaveZone", "SaveLocation", "SaveGeometry", "DecommissionLocation", "Claim", "Do",
	"Drain", "PublishPolicyDeclared", "PublishTripEvents"}

// agentWriteVerbAllowances maps an exact selector expression (types.ExprString) to why it is not a
// write. Empty: no read of either surface needs a verb name.
var agentWriteVerbAllowances = map[string]string{}
