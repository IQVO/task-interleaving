# ADR 0010: Adopt the fleet REST API guideline

## Status

Accepted (2026-11-01). Follows [ADR 0005](0005-persistence-and-evaluation-semantics.md) (the
transactional idempotency design the middleware keeps) and
[ADR 0006](0006-postgres-and-kafka-are-the-only-runtime.md) (`Idempotency-Key` is required on every
deployment). Implements the fleet standard "Warehouse REST API Guideline" (warehouse-docs); it is a
breaking change that ships with the whole REST cutover set.

## Context

The 2026-10-30 application portfolio assessment found two spellings of the replay header, key reuse
answered `422` in most services and `409` in one, three command-URL styles and unpaginated lists.
This service already had noun-only routes (no `:verb` suffix, no verb segment), already kept its only
POST (`POST /recommendations`, a read-only simulation) behind the route-scoped `Idempotency`
middleware, and already paged its only collection (`GET /hints`) with `limit`/`cursor` and
`{items, nextCursor}`. What it lacked: a replay marker, `409` for key reuse, an OpenAPI document that
described them, and a declared status for two list-shaped reads that are not collections.

## Decision

The service follows the guideline, with no alias, no toggle and no coexistence of old and new:

1. **URLs name things, never actions (R1, R2).** No route changes: there is nothing to rename and no
   operation id moves, so MCP and A2A mappings are untouched. Status codes and bodies of every
   existing operation are unchanged.
2. **Every POST and PATCH is idempotent (R3 to R6, R8).** `POST /recommendations` was already wrapped;
   there is no PATCH. The middleware changed minimally:
   - the replay header is `Idempotency-Replayed: true`, set on every replay (a stored 2xx or 4xx, and
     the winner's answer handed to the loser of a race) and never on a first execution;
   - the same key with a different method, path or body is `409 Conflict`, problem type
     `idempotency-key-reused` (it was `422`); the fingerprint is method + path + SHA-256 of the body;
   - a missing key is `400 idempotency-key-required`; a key that is not 1 to 128 printable ASCII
     characters is `400 idempotency-key-invalid` (an over-long key used to be reported as
     `idempotency-key-required`);
   - a 5xx is rolled back and never stored, and the key row commits in the same transaction as the
     aggregate change and its outbox rows (unchanged).
3. **OpenAPI (R3 to R5, R8).** `POST /recommendations` documents `400`, `409` (the shared `Conflict`
   response, which also covers `concurrent-modification` of the policy `PUT`) and
   `Idempotency-Replayed` (`components/headers/IdempotencyReplayed`) on its 200; the `422` response no
   longer describes key reuse.
4. **Collections (R7).** `GET /hints` already pages: `limit` (1 to 500, default 100), opaque `cursor`,
   keyset order `(issued_at, hint_id)` descending, `{items, nextCursor}`, `400 invalid-limit` /
   `invalid-cursor`. Nothing changed.
5. **Enforcement.** `.spectral.yaml` runs the shared `rest-guideline` rule
   (`.spectral/functions/restGuideline.js`) in the existing `api-lint` job with
   `--fail-severity=warn`; the baseline was 5 findings (R4, R5, R7 x2, R8) and is 0.

### Exemptions

- `GET /sites/{siteId}/policy`: `x-pagination-exempt: view`. It returns one composite resource, the
  site's current policy, whose representation merely contains an array (`eligibleTaskTypes`).
- `GET /reports/hint-adoption`: `x-pagination-exempt: report`. A read model bounded by a `from`/`to`
  window of at most 366 days at a fixed `day` granularity, served by the separate `ti-reports` binary.
  The ruleset already skips `/reports/` structurally; the marker makes the exemption explicit.
- `GET /reports/freshness` is a singleton and needs no marker.

### Consumers changed in the same change

None needed. The only HTTP client in this repository is its own web remote (`web/src/api.ts`): it
issues only `GET` and the idempotent-by-method `PUT /sites/{siteId}/policy`, no POST or PATCH, and it
already reads `GET /hints` from `items` and follows `nextCursor`. The service calls no sibling over
HTTP, and the MCP and A2A adapters call use cases directly, so there is no outbound adapter, seed
script or recorded fixture to migrate.

## Consequences

- **Breaking.** Clients that treated `422` as the key-reuse answer, or that relied on an over-long key
  being `idempotency-key-required`, must change. This PR must deploy together with the REST cutover
  set of the fleet.
- A client may now distinguish a replay from a first execution by `Idempotency-Replayed: true`, and a
  key-reuse bug from a business `409` by the problem type `idempotency-key-reused`.
- The generated REST reference (`docs/docs/api-reference/rest`) is regenerated from the spec with the
  CI recipe.
