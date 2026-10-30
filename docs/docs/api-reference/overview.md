---
id: overview
title: API overview
slug: /api-reference
sidebar_position: 1
---

# API overview

:::info Implemented by cmd/api
`cmd/api` serves every route below (`internal/adapters/inbound/http`), on
`HTTP_ADDR` (default `:8080`). The contracts are `apis/openapi.yaml` and
`apis/asyncapi.yaml`, pinned by
[ADR 0001](/docs/adr/0001-task-interleaving-bounded-context) and
[ADR 0002](/docs/adr/0002-stationtrip-and-policy-aggregates). There is no route in
the code that the spec does not list: the router registers exactly the six API
routes plus `/healthz` and `/readyz`. See the [Quickstart](/docs/overview/quickstart)
for real responses.
:::

| Interface | Source of truth | Notes |
| --- | --- | --- |
| REST | `apis/openapi.yaml` (OpenAPI 3.0.3, version 1.0.0) | The pages under *REST* are generated from the spec with `npm run gen-api-docs`; do not edit them by hand. Servers named in the spec: `http://localhost:8080` (local) and `http://localhost:8000/api/task-interleaving` (kind cluster through Kong). |
| Events | `apis/asyncapi.yaml` (AsyncAPI 2.6.0, version 1.0.0) | Summarised on the [event catalogue](/docs/api-reference/events); every field on [Domain events](/docs/ddd/domain-events). |
| MCP | [ADR 0007](/docs/adr/0007-mcp-server-adoption), `internal/adapters/inbound/mcp` | `cmd/mcp` serves Streamable HTTP on `/mcp` (port 8090; through Kong `/api/mcp/task-interleaving`). Four read-only tools, no write tool: `get_site_policy`, `list_hints` (`stationId`, `state`, `siteId`), `get_hint`, `get_station` (position and active hint). No auth, by fleet decision. |
| A2A | [ADR 0009](/docs/adr/0009-a2a-inbound-adapter), `internal/adapters/inbound/a2a` | `cmd/a2a` serves JSON-RPC 2.0 on `/` and `/a2a` (port 8093; through Kong `/api/a2a/task-interleaving`), the Agent Card at `/.well-known/agent-card.json`. Two read-only skills, no write skill: `station_hint_status` (`stationId`) and `site_interleaving_overview` (`siteId`, `limit` 1..20). Domain rejections are answered in the message envelope as RFC 7807 problems with the REST slugs, never as JSON-RPC errors. No auth, by fleet decision. |

## Endpoints

| Tag | Endpoint | Operation id | Success |
| --- | --- | --- | --- |
| Policy | `PUT /sites/{siteId}/policy` | `declareInterleavingPolicy` | `200`, the policy at its new version, `ETag: "<version>"` |
| Policy | `GET /sites/{siteId}/policy` | `getInterleavingPolicy` | `200`, the policy; `404 policy-not-found` for an inert site |
| Hints | `GET /hints` | `listHints` | `200 {items, nextCursor?}`, newest first, filters `stationId`, `siteId`, `state` |
| Hints | `GET /hints/{hintId}` | `getHint` | `200`, the hint with its candidates and outcome |
| Stations | `GET /stations/{stationId}` | `getStation` | `200`, position, last completed task, Active hint; `404 station-not-found` |
| Recommendations | `POST /recommendations` | `recommendTasks` | `200`, ranked candidates, possibly empty; read-only simulation |
| Reports | `GET /reports/hint-adoption`, `GET /reports/freshness` | `getHintAdoption`, `getReportsFreshness` | `200`, served by `cmd/ti-reports` (port 8092, in-cluster only) from the analytical database; see [ADR 0008](/docs/adr/0008-analytics-read-side) |
| Health | `GET /healthz`, `GET /readyz` | `healthz`, `readyz` | `200` / `200`, or `503` once shutdown has started (the spec also says "while starting"; the listener only opens after boot, see [Runbook](/docs/operations/runbook#probes)) |

## Conventions in the contract

- **Policy versioning.** A policy carries `version` (1 at declaration, +1 per
  accepted change) and a strong `ETag` equal to the quoted version. The first
  declaration sends no `If-Match`; a replacement must send the current one, and a
  stale or missing value is `412 version-mismatch`. A write that loses a race at
  the database is `409 concurrent-modification`. Declaring identical values again
  still advances the version.
- **Paging.** `GET /hints` takes `limit` (1 to 500, default 100) and the opaque
  `cursor` (the previous page's `nextCursor`). No `nextCursor` marks the last page.
- **`Idempotency-Key`.** Documented on `POST /recommendations` and implemented by
  `internal/adapters/inbound/http/idempotency.go`. It is required on every
  deployment, since Postgres is the only runtime (`400
  idempotency-key-required` when absent or longer
  than 128 characters); the same key with a different method, path or body is
  `422 idempotency-key-reused`; an equal request replays the stored answer.
- **Errors** are RFC 7807 `application/problem+json` with
  `type = https://errors.task-interleaving.warehouse-systems.dev/<slug>`. The slugs
  in the spec: `malformed-request`, `invalid-site-id`, `invalid-station-id`,
  `invalid-hint-id`, `invalid-policy`, `invalid-task-type`, `invalid-cursor`,
  `invalid-limit`, `invalid-state`, `idempotency-key-required`,
  `idempotency-key-reused`, `policy-not-declared`, `origin-unknown`,
  `station-position-unknown`, `policy-not-found`, `hint-not-found`,
  `station-not-found`, `version-mismatch`, `concurrent-modification` and
  `internal-error`. The errors that map to these slugs are listed on
  [Use cases](/docs/ddd/use-cases#errors-and-the-problem-types-they-become) and, with
  remedies, on [Troubleshooting](/docs/operations/troubleshooting#rest-problem-types).
- **Auth**: none. The fleet reverted REST and MCP auth on 2026-09-11, and
  `TestNoAuthMiddlewareReintroduced` fails CI if auth middleware returns under
  `internal/adapters/inbound`.
- **Advisory by contract.** Nothing in this API assigns work to a worker;
  `fulfillment-execution` keeps `claimNext` as its only claim path
  ([ADR 0001](/docs/adr/0001-task-interleaving-bounded-context)).
