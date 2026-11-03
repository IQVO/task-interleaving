# ADR 0011: Agent authority levels and complete MCP surface

## Status

Accepted (2026-11-02). Extends [ADR 0007](./0007-mcp-server-adoption.md) (the
read-only MCP server) and [ADR 0009](./0009-a2a-inbound-adapter.md) (the A2A
adapter); adds the Agentic Standard v2 declarations to both.

## Context

The warehouse fleet adopted the Agentic Standard v2 authority ladder
(`warehouse-docs` `agents/fleet/agent-authority.md`): every MCP tool and every
A2A skill declares exactly one level — L1 reads facts, L2 computes a
recommendation that changes nothing, L3 mutates state, L4 is a bounded
auto-act envelope an ADR grants — so a caller knows what gating to apply
before invoking. The declaration is data, not prose: MCP tools carry it in
`_meta` under the key `com.warehouse/authority` (returned verbatim by
`tools/list`), A2A skills carry it as a tag `authority:L1`..`authority:L4`.
The standard also asks two more things of an MCP server: a stateless
Streamable HTTP transport (no reliance on `Mcp-Session-Id`, AS-4) and a
surface beyond tools — at least one resource and one prompt (AS-3).

This context's agent surfaces are read-only by construction (ADR 0007,
ADR 0009): four MCP tools (`get_site_policy`, `list_hints`, `get_hint`,
`get_station`) and two A2A skills (`station_hint_status`,
`site_interleaving_overview`), all pure reads of this context's own state.
No tool computes a proposal (the recommender is a REST simulation) and
nothing writes.

## Decision

- **Authority (AS-1).** Every MCP tool declares
  `Meta: authorityMeta(authorityL1)` under `com.warehouse/authority`, with the
  fleet's pinned const ladder (`authorityMetaKey`, `authorityL1..L4`) in
  `internal/adapters/inbound/mcp/authority.go`. Both A2A skills carry the tag
  `authority:L1`. The whole surface is L1: a pure read of this context's own
  state. There is no L2 (no tool recommends — the ranker runs inside the
  service on REST `POST /recommendations`, and the tools only read its
  output), no L3/L4 (no tool or skill writes; ADR 0007's charter stays
  read-only) and no `initiated_by` attribution (that is stamped only by
  write paths, which this context does not expose over MCP/A2A).
- **Stateless transport (AS-4).** The Streamable HTTP handler is built with
  `&mcp.StreamableHTTPOptions{Stateless: true}`: no session id is issued, the
  server answers the 2026-07-28 revision's per-request handshake
  (`server/discover`) and a legacy `initialize` still answers 200. Pinned by
  raw-POST tests (`stateless_test.go`); reverting the option to nil fails
  them.
- **Complete surface (AS-3).** Exactly one resource and one prompt join the
  tools, both read-only by nature and both built on existing read use cases:
  - `warehouse://task-interleaving/policies/{siteId}` (resource template,
    `application/json`): the interleaving policy ONE site declared — the same
    `GetPolicy` use case as the `get_site_policy` tool and the REST GET. A
    scoped read model (one declaration, eight fields), never a table dump.
  - `advise_next_task` (prompt): this context's operator SOP — read the
    station and the hint it holds, the full hint record, the site's policy
    envelope, then interpret (a hint is a suggestion, never an assignment).
    It takes one typed, required argument (`stationId`), names tools and
    never calls them, and cannot write.
- **Enforcement.** The template's static sensor
  `internal/architecture/mcp_authority_fitness_test.go` (unmanaged fleet
  copy) fails the build when a tool or skill misses its declaration, when a
  declared level contradicts the read/write annotations, or when the server
  offers tools but no resource and no prompt. The golden registry
  `testdata/tool_registry.golden.json` pins every tool WITH its `_meta`
  authority, so a level change is a reviewed golden diff.

The tool and skill names, arguments and results are unchanged; the additions
are declarations and new read-only surface, not behaviour.

## Consequences

- A model host can gate on `com.warehouse/authority` / `authority:L1` without
  knowing this context: everything here is safe to call without approval.
- The ops tooling keeps working against the 2026-07-28 revision
  (`server/discover`) and against legacy initialize clients, with no session
  state anywhere.
- Adding ANY write tool later is a new ADR (it would be L3, carry
  `initiated_by` attribution per the standard, and amend ADR 0007's
  read-only charter) plus a golden and sensor-visible diff.
- The unused `authorityL2..L4` constants stay (with lint suppressions): the
  fleet's declaration shape is one ladder in every repo.
