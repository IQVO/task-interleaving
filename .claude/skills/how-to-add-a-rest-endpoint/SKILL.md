---
name: how-to-add-a-rest-endpoint
description: Add or change a REST endpoint in this service in the fleet's hexagonal order (domain invariant, use case, port, HTTP adapter, apis/openapi.yaml, godog scenario). Use when touching the HTTP adapter, apis/openapi.yaml, or exposing a use case over HTTP.
---

# How to add a REST endpoint

Use when asked to add a new REST use case/endpoint to this service. Follow
this order — domain first, adapter last — never the reverse; writing the
HTTP handler before the domain invariant it enforces produces handlers
that validate nothing and use cases that get bypassed.

**State of this repo.** Contracts and domain are committed (wave 1); the
application layer, the HTTP adapter and the composition root under `cmd/` are
built in wave 2 and do not exist yet, so steps 2-3 below describe where code
goes rather than pointing at files. The worked example is `GET
/stations/{stationId}` (`operationId: getStation` in `apis/openapi.yaml`): it
reads the `stationtrip.StationTrip` aggregate and returns its position and
Active hint. Read the route table in `.claude/rules/rest-api.md` alongside this
guide; keep it in sync when you add a route.

## 1. Domain first: does an invariant already exist, or do you need one?

Check the four packages under `internal/domain/` (`policy`, `travel`,
`recommend`, `stationtrip`) for the rule this endpoint enforces. A REST
endpoint should almost never contain business logic itself — it decodes a
request, calls a use case, encodes the result. If the operation needs a new
domain rule (the way `PUT /sites/{siteId}/policy` needs "`maxCandidates` is 1
to 5", which lives in `policy.Settings` validation and is refused with
`policy.ErrInvalidCandidates`), add it to the aggregate or value object, with a
table-driven test in the sibling `_test.go` file (see
`internal/domain/policy/policy_test.go`), BEFORE touching the application or
adapter layers. Remember the boundary values (see `.claude/skills/how-to-test/SKILL.md`).

## 2. Application: define the use case (application layer, wave 2)

One use case per file in the application layer, named after the
`operationId` in PascalCase (`GetStation`, `DeclareInterleavingPolicy`,
`RecommendTasks`). Shape:

```go
// GetStation — one sentence: what business capability this represents.
type GetStation struct {
    Trips ports.StationTripReader // driven ports only — never a concrete adapter
    Clock ports.Clock             // if it needs "now" (never call time.Now() directly)
}

func (uc *GetStation) Execute(ctx context.Context, id stationtrip.StationID) (StationView, error) {
    // 1. load the aggregate (or read model) via the port
    // 2. let the aggregate apply the rule (never inline the invariant here)
    // 3. persist via the port, writing events to the outbox in the SAME transaction
    // 4. return a domain-typed result, not a DTO
}
```

Driven ports are interfaces ONLY, defined in the application layer; the
architecture tests in `internal/architecture` keep adapters and domain from
importing each other. Unit-test use cases against an in-memory adapter, never a
real Postgres/HTTP call, and cover the success path AND the domain-rule failure
path. A command that changes a mutable resource (the policy) takes the expected
version for the `If-Match` check and returns the new version for the `ETag`.

## 3. Adapter: wire the HTTP handler (HTTP adapter, wave 2)

In the inbound HTTP adapter:

1. Request/response DTOs live ONLY in the adapter (camelCase JSON as in
   `apis/openapi.yaml`); domain types never carry JSON tags.
2. The handler decodes and validates, converting to domain value objects
   immediately (`stationtrip.NewStationID`, `policy.NewSiteID`) — a bad value
   fails here as an RFC 7807 `400` (`invalid-station-id`), never reaching the
   use case; calls `Execute`; maps use-case errors to a problem type through ONE
   error-mapping table (add a row, don't hand-roll a status); encodes the
   result.
3. Honour the cross-cutting rules: `Idempotency-Key` on POST, `ETag` /
   `If-Match` on versioned resources, no auth middleware (fleet decision
   2026-09-11, `TestNoAuthMiddlewareReintroduced`).
4. Wire the new use case in the composition root under `cmd/` — the only place
   that imports every layer.

Write at least one httptest per endpoint: one success path, one error path
(validation failure AND/OR the domain-rule failure).

## 4. Contract: update OpenAPI

Add the path to `apis/openapi.yaml` with request/response schemas and an RFC
7807 `application/problem+json` response for each error case, following the
existing entries (`Problem`, `BadRequest`, `Unprocessable`, `VersionMismatch`).
The error `type` is `https://errors.task-interleaving.warehouse-systems.dev/<slug>`.
Lint it with the repo's spectral ruleset (`.spectral.yaml`). Then update
`.claude/rules/rest-api.md`.

## 5. Behaviour: add a godog scenario

If the endpoint is user-facing behaviour, add a Gherkin `.feature` file under `features/`
for the `make bdd` target. The suite (`features_test.go`) drives the real use
cases over the in-memory adapters, not the HTTP server, so also add an httptest
in `internal/adapters/inbound/http` and a shape check against `apis/openapi.yaml`
(`shape_test.go`).

## 6. Verify before opening the PR

```bash
make check       # fmt-check vet build lint test
make check-all   # + coverage (90% gate) + arch-test + bdd
```

`make coverage` gates `./internal/domain/...,./internal/application/...` at 90%
— a new use case with no test on its failure path is the most common way to
miss this gate.
