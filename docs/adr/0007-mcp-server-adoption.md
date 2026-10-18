# ADR 0007: a read-only MCP server

## Status

Accepted (2026-10-18).

## Context

Every other bounded context of the fleet that exposes an HTTP API also ships an
MCP server so that agents (warehouse-ops-agent first) can read its data without
a bespoke REST client. task-interleaving had none: CLAUDE.md rule 9 recorded
"MCP is additive and not built yet (a separate binary with read tools only,
when it comes)". The service already has the read use cases behind its REST
adapter (`GetPolicy`, `ListHints`, `GetHint`, `GetStation`), so an MCP adapter
is another driving adapter over them, not new domain.

## Decision

- **A second driving adapter, a separate binary.** `internal/adapters/inbound/mcp`
  is built on the official MCP Go SDK and served over Streamable HTTP only (no
  stdio, no SSE) by `cmd/mcp`, mounted at `/mcp` and `/` with an open
  `/healthz`. It is a second deployable beside `cmd/api`; the Helm chart gets an
  mcp Deployment and Service.
- **Four read tools over the existing queries, nothing else.**
  `get_site_policy(siteId)`, `list_hints(stationId?, siteId?, state?, limit?,
  cursor?)`, `get_hint(hintId)` and `get_station(stationId)` (position plus
  active hint). Arguments and results are lowerCamelCase like the REST contract;
  typed errors carry the REST problem slug (`policy-not-found`,
  `hint-not-found`, `station-not-found`, `invalid-*`) as the prefix of the tool
  error. `POST /recommendations` is a simulation and stays REST-only.
- **Read-only is structural.** The adapter's `Deps` holds the four read use
  cases and nothing that can write; the adapter and `cmd/mcp` import no `ports`
  package, no event sink, no outbox, no Kafka adapter and no expiry worker.
  `cmd/mcp` never dials Kafka and never runs the consumers, the relay or the
  expiry worker: those stay in `cmd/api`. `TestMCPServerIsReadOnly` pins the
  imports, the `Deps` fields and the use cases `cmd/mcp` may name;
  `TestToolSurface` pins the exact tool set (budget 4), the read-only
  annotations and the absence of write verbs in tool names;
  `TestToolRegistryGolden` pins the whole advertised registry;
  the integration test proves the binary reads what `cmd/api`'s use cases wrote
  in a real Postgres and adds no outbox row.
- **No sibling calls, no auth.** The server reads this context's own tables
  only (the event-fed local copies of ADR 0003 are not exposed), calls no other
  context over REST or MCP, and has no auth layer (the fleet-wide revert of
  2026-09-11; `TestNoAuthMiddlewareReintroduced`). Access control is the
  in-cluster ClusterIP boundary.
- **Boot.** `DATABASE_URL` is mandatory (no in-memory mode, ADR 0006); the
  binary runs the idempotent embedded migrations on start (with
  `MIGRATIONS_DATABASE_URL` for a direct DSN behind PgBouncer) so it can start
  against a fresh database before `cmd/api` has.

## Consequences

- Agents can answer "what policy does SITE-1 have, which hint does STN-07 hold,
  why did the last hint expire" without REST glue.
- Adding a tool is a reviewed act: the tool budget, the curated list and the
  golden file all change together (an amendment of this ADR).
- Declaring a policy stays a REST operation with `If-Match`; the hint lifecycle
  stays with the consumers and the expiry worker.
