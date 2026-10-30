# ADR 0009: A2A inbound adapter

## Status

Accepted (2026-10-29). Follows [ADR 0007](0007-mcp-server-adoption.md) (the
read-only MCP adapter this one is modelled on and whose charter it inherits),
[ADR 0004](0004-cloudevents-envelope-and-type-catalogue.md) (CloudEvents on every
Kafka message) and [ADR 0006](0006-postgres-and-kafka-are-the-only-runtime.md)
(Postgres and Kafka are the only runtime). Mirrors the fleet's A2A rollout
(reference: workforce-management ADR 0039, wes-work-planning ADR 0046).
Additive: the REST API, the MCP server, every payload and the outbox are
unchanged.

## Context

The fleet is adopting the Agent2Agent (A2A) protocol so that a bounded context
can **delegate** a goal to another context's agent. This context already
exposes its read use cases over REST and, since ADR 0007, over MCP. Three
transports now carry three different things and must not overlap:

| Transport | Carries |
| --- | --- |
| Kafka + CloudEvents | **facts** between contexts (ADR 0004) |
| MCP | an agent's **tools** |
| A2A | **delegation** of a goal to another context's agent |

The write gate of the A2A rollout says a skill may change state only if the
context's MCP surface already offers that write. The MCP charter of this
context (ADR 0007) is read-only by construction: four read tools, no write
use case reachable, `TestMCPServerIsReadOnly`. Declaring a policy is a REST
operation with `If-Match`; the hint lifecycle belongs to the consumers and the
expiry worker; `POST /recommendations` is a simulation that stays REST-only.
So this agent is **read-only**, and widening what the context exposes is out of
scope.

## Decision

- **A third driving adapter, a third binary.** `internal/adapters/inbound/a2a`
  is built on the official `a2aproject/a2a-go/v2` SDK (pinned v2.7.0) and
  served by `cmd/a2a`: JSON-RPC 2.0 over HTTP on **8093** (`A2A_ADDR`), the
  endpoint mounted at `/` and `/a2a`, the Agent Card at
  `/.well-known/agent-card.json`, an open `/healthz`. It is composed like
  `cmd/mcp` over the SAME read use cases (`GetPolicy`, `ListHints`,
  `GetStation`), with the same Postgres repositories and idempotent boot
  migrations (`DATABASE_URL`, `MIGRATIONS_DATABASE_URL`).
- **`A2A_AGENT_URL` is mandatory.** The card advertises it, so it must be an
  absolute http(s) URL a delegating agent can dial; the binary refuses to boot
  otherwise, before it dials or binds anything. The chart defaults it to the
  release's in-cluster Service DNS (`http://<fullname>-a2a.<ns>.svc.cluster.local:8093/`)
  and `a2a.agentUrl` overrides it.
- **Two curated, goal-shaped read skills, derived from the MCP tools** (not one
  skill per endpoint, not one per tool):

  | Skill | Kind | What it answers |
  | --- | --- | --- |
  | `station_hint_status(stationId)` | read | where a station stands and what it should take next: site, position, last completed task, and the hint it holds with its ranked candidates (the `get_station` tool). A station that never recorded a completion is `station-not-found`. |
  | `site_interleaving_overview(siteId, limit?)` | read | how interleaving is set up and behaving at a site: the declared policy plus the newest hints (window 1..20, default 5) with a count per state (`get_site_policy` composed with `list_hints`). A site with no policy is inert and is `policy-not-found`. |

  `get_hint` has no skill (it is a lookup by id, not a goal) and pagination is
  not offered: the window is bounded and `hasMore` says when it is truncated.
- **Message-only, answered in-band.** Both skills are request/response reads, so
  there is no Task lifecycle. Domain rejections and malformed requests are
  ANSWERED as RFC 7807-shaped `problem`s inside the message envelope, never as
  JSON-RPC errors, with the **same classification and slugs as the REST
  adapter's problem catalogue** restricted to what a read can raise: `404`
  `policy-not-found` / `station-not-found`, `400` `invalid-site-id` /
  `invalid-station-id` / `invalid-limit`, `500` `internal-error` (REST's
  fallback). The `type` URI is the REST one
  (`https://errors.task-interleaving.warehouse-systems.dev/<slug>`). Request-shape
  problems add `malformed-skill-request`, `unknown-skill` and `invalid-argument`
  (all `400`). Details are adapter-owned text: a domain error's own message
  (package prefixes, wrapped storage errors) never reaches the caller, and an
  unclassified failure is logged server-side and answered generically. REST
  has no 409/503 for a read, so none is answered here; `412`/`422` are REST
  write/simulation outcomes with no A2A counterpart.
- **Strict input.** The first message part carries `{"skill": "...",
  "arguments": {...}}` as a Data part, or a Text part holding the same JSON.
  Unknown fields are rejected and trailing data is refused, so a misspelled
  `lmit` cannot silently drop the window size (`encoding/json` matches names
  case-insensitively, so only genuinely unknown names are refused). `limit` is a
  pointer: an explicit `0` is refused, not read as "default". No identifier of
  this context has a deprecated alias, so there is no alias text to hide.
- **Read-only is structural, like MCP.** The adapter's `Deps` holds read use
  cases and nothing that can write; the adapter and `cmd/a2a` import no `ports`
  package, event sink, outbox, Kafka adapter or expiry worker and name no write
  use case. `cmd/a2a` never dials Kafka and starts no relay, consumer or
  expiry worker: those stay in `cmd/api` (so there is no event publisher here to
  keep silent). `TestA2AServerIsReadOnly` pins this with the MCP sensor's
  detectors, `TestA2AAdapterDependencyRule` pins that the adapter depends only
  on application and domain and that nothing depends on it, and the card test
  pins that no skill id carries a write verb. The integration test (build tag
  `integration`) proves on real Postgres that the A2A reads return what the
  write use cases persisted and add no outbox row, published or not.
- **No sibling calls, no auth.** The surface reads this context's own tables
  only (the event-fed local copies of ADR 0003 are not exposed), makes no REST,
  MCP or A2A call to any sibling context (CLAUDE.md rule 6) and has no auth
  layer: the card declares no security schemes, by the same fleet-wide decision
  (revert of 2026-09-11, `TestNoAuthMiddlewareReintroduced`). Access control is
  the in-cluster ClusterIP boundary. Each skill runs in an OTel span
  `a2a.skill <id>` carrying the `a2a.skill` attribute (the global tracer: this
  service configures no exporter, so, like everything else in it, the spans
  are no-ops until one is wired; see the Observability page).

## Consequences

- A delegating agent can ask "what should STN-07 take next" and "how is SITE-1
  interleaving going" with one message each, and gets either a structured
  answer plus a one-line summary or a `problem` it can branch on.
- A fourth deployable (`/app/a2a`) in the same image and an `a2a.*` chart block
  (`<release>-a2a`, default **off**), exposed through Kong at
  `/api/a2a/task-interleaving` rewritten to `/`. Port 8093 is the fleet's A2A
  port (8090 mcp, 8091 projector admin, 8092 reports).
- Adding a write skill later is a reviewed act: it requires the MCP surface to
  offer that write first (an amendment of ADR 0007), and a write would go
  through the same use case and transactional outbox as REST, the fact still
  leaving as a CloudEvent. A2A never becomes a data-sync channel.
- Delegating to this agent does not change who decides: a hint stays advisory
  (CLAUDE.md rule 4); fulfillment-execution, which owns band ordering and
  `claimNext`, is the only context that assigns tasks.
