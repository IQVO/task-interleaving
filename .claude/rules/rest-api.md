---
paths:
  - "internal/adapters/inbound/http/**"
  - "apis/openapi*.yaml"
  - "apis/openapi/**"
---

# REST API (inbound adapter)

The contract is `apis/openapi.yaml` (OpenAPI 3.0.3, base URL
`http://localhost:8080`, via Kong `http://localhost:8000/api/task-interleaving`).
This file is documentation for an agent, not generated: keep it in sync with the
spec by hand. The adapter is `internal/adapters/inbound/http` (chi router, one
handler file per resource); `TestRouterServesExactlyTheDocumentedOperations`,
`TestProblemSlugsMatchTheContract` and `TestResponsesHaveTheDocumentedShape` fail
when the router, the problem slugs or the response shapes drift from the spec.

| Route | operationId | Use case |
| --- | --- | --- |
| `PUT /sites/{siteId}/policy` | `declareInterleavingPolicy` | `DeclarePolicy` |
| `GET /sites/{siteId}/policy` | `getInterleavingPolicy` | `GetPolicy` |
| `GET /hints` | `listHints` | `ListHints` (filters `stationId`, `siteId`, `state`; `limit` 1..500, default 100; opaque `cursor`) |
| `GET /hints/{hintId}` | `getHint` | `GetHint` |
| `GET /stations/{stationId}` | `getStation` | `GetStation` |
| `POST /recommendations` | `recommendTasks` | `Recommend` (read-only simulation: no state change, no event; behind the `Idempotency-Key` middleware) |
| `GET /healthz`, `GET /readyz` | `healthz`, `readyz` | probes; `/readyz` is 503 while starting or draining |

Hints are issued by the event consumers, never by a REST call; nothing in this
API assigns work to a worker.

## Conventions

- Error shape: RFC 7807 `application/problem+json` with `type`, `title`, `status`,
  `detail`, `instance`; `type` is
  `https://errors.task-interleaving.warehouse-systems.dev/<slug>`.
- `400`: malformed JSON or unknown field (`malformed-request`), invalid path or
  query value (`invalid-site-id`, `invalid-station-id`, `invalid-hint-id`,
  `invalid-cursor`, `invalid-limit`, `invalid-state`), invalid policy values
  (`invalid-policy`, `invalid-task-type`), missing `Idempotency-Key`
  (`idempotency-key-required`).
- `404`: `policy-not-found` (an inert site), `hint-not-found`, `station-not-found`.
- `409` `concurrent-modification`: a write lost a race at the database.
- `412` `version-mismatch`: `If-Match` missing or stale on an existing policy. An
  `If-Match` that is not a version (weak tag, `*`, garbage) can match no version and
  gets the same 412 (the contract documents no other answer); the first declaration
  of a site sends none, and an `If-Match` on it is a 412 too. `If-Match` is read
  quoted (`"2"`) or bare (`2`).
- `422`: `policy-not-declared`, `origin-unknown`, `station-position-unknown`,
  `idempotency-key-reused` (same key, different body).
- `500` `internal-error`.
- Optimistic concurrency: mutable resources carry `version` (starts at 1, +1 per
  accepted change) and a strong `ETag: "<version>"`. The first policy declaration
  sends no `If-Match`; replacing an existing policy requires `If-Match` with the
  current version. Declaring identical values still advances the version.
- `400` `malformed-request` also covers a `fromLocation` that is present but empty,
  blank or longer than 128 characters (the request schema's bounds); a blank or
  invalid `stationId` is `invalid-station-id`.
- `Idempotency-Key` (UUID per logical submit) is required on every POST in every
  deployment (Postgres is the only runtime); a replay with the same key and body
  returns the original response. The spec marks the header `required: true`.
- JSON is camelCase on REST (`maxDetourM`, `hintTtlSeconds`); the same fields are
  snake_case in CloudEvents `data`.
- Auth: none. The fleet-wide REST+MCP auth was deliberately reverted on
  2026-09-11 and this API is unauthenticated pending a fresh decision;
  `TestNoAuthMiddlewareReintroduced` in `internal/architecture/fitness_test.go`
  enforces that no auth middleware comes back.
