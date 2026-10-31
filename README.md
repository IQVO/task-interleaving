# task-interleaving

[![CI](https://github.com/IQVO/task-interleaving/actions/workflows/ci.yml/badge.svg?branch=develop)](https://github.com/IQVO/task-interleaving/actions/workflows/ci.yml?query=branch%3Adevelop) [![coverage gate](https://img.shields.io/badge/coverage%20gate-90%25-brightgreen)](https://github.com/IQVO/task-interleaving/blob/develop/Makefile) [![Go](https://img.shields.io/badge/go-1.27.3-00ADD8?logo=go&logoColor=white)](https://github.com/IQVO/task-interleaving/blob/develop/go.mod) [![release](https://img.shields.io/github/v/release/IQVO/task-interleaving?sort=semver)](https://github.com/IQVO/task-interleaving/releases) [![docs](https://img.shields.io/badge/docs-iqvo.github.io-blue)](https://iqvo.github.io/task-interleaving/) [![license](https://img.shields.io/badge/license-MIT-blue)](https://github.com/IQVO/task-interleaving/blob/develop/LICENSE)

Task Interleaving: a WES-tier **Supporting** bounded context of the
warehouse-systems fleet. When a station completes a task it ranks the site's
open tasks by travel distance and issues an **advisory hint**, so operators
chain compatible work instead of travelling empty. It does **not** own task
execution — `fulfillment-execution` stays pull-based and uses the hint only as
a tie-break inside one priority band — and it does not own the task pool or
the warehouse map: it keeps consumed copies of both (ADR 0003) and never
writes upstream. Hexagonal Go, Postgres, REST, Kafka (CloudEvents 1.0).
Scaffolded from `warehouse-harness-template` (v3) — guides in
`.claude/rules/`, sensors in `HARNESS.md`; design and pinned contracts in
`docs/adr/`. On `develop` (merged in PR #7).

## Disclaimer

This project is a domain-driven design study. It is not associated with,
endorsed by or affiliated with any company, and it uses no company's code,
data, logos or trademarks. All names, figures and examples are illustrative
or synthetic.

---

## Documentation

Full documentation is published at
**<https://iqvo.github.io/task-interleaving/>** (built from `docs/` by
`.github/workflows/docs.yml`; pull requests build it, `main` deploys it).
Build locally: `cd docs && npm ci && npm run build`.

Page map (source under `docs/docs/`):

| Area | Pages |
|---|---|
| Overview | [Introduction](docs/docs/overview/introduction.md), [Architecture](docs/docs/overview/architecture.md), [Quickstart](docs/docs/overview/quickstart.md) |
| Operations | [Configuration](docs/docs/operations/configuration.md), [Runbook](docs/docs/operations/runbook.md), [Observability](docs/docs/operations/observability.md), [Troubleshooting](docs/docs/operations/troubleshooting.md) |
| Development | [Testing](docs/docs/development/testing.md) |
| Ecosystem | [Integration](docs/docs/ecosystem/integration.md) |
| DDD | [Use cases](docs/docs/ddd/use-cases.md) (the 14 application use cases), [Subdomain classification](docs/docs/ddd/subdomain-classification.md), [DDD artifacts](docs/docs/ddd/ddd-artifacts.md) (the 11-page ddd-crew pack) |
| API reference | [REST](docs/docs/api-reference/overview.md) (generated from `apis/openapi.yaml`), [Events](docs/docs/api-reference/events.md) |
| Decisions | [ADR index](docs/adr/index.md) — decisions 0001 to 0009 with status |

The MCP server, the analytics read side and the A2A server are described in
ADRs [0007](docs/adr/0007-mcp-server-adoption.md),
[0008](docs/adr/0008-analytics-read-side.md) and
[0009](docs/adr/0009-a2a-inbound-adapter.md).

---

## Architecture

Hexagonal / ports & adapters; dependencies point inward only.

```text
apis/                 openapi.yaml, asyncapi.yaml (the pinned contracts)
charts/task-interleaving/  Helm chart (values, templates, tests/), plus the root Dockerfile
cmd/api/              the composition root (config, adapters, consumers, relay, topics)
cmd/mcp/              read-only MCP server (ADR 0007)
cmd/a2a/              read-only A2A server (composition root, router; ADR 0009)
cmd/ti-projector/     analytics projector — the only analytical-DB writer (ADR 0008)
cmd/ti-reports/       analytics reader (GET /reports/...)
migrations/           embedded SQL migrations (0001_interleaving_schema)
features/             godog scenarios (6 .feature files, 68 scenarios) and their runner and steps (package features_test); the repo root holds no .go file
docs/                 Docusaurus site (docs/docs) and the ADRs (docs/adr)
internal/domain/      policy, travel, recommend, stationtrip
internal/application/ usecases, ports, repository, outbox
internal/adapters/    inbound/{http,kafka,expiry,mcp,a2a}, outbound/{postgres,memory,events,outbox,kafka,system}, kafka/cloudevents
internal/architecture hexagonal and fleet fitness tests
internal/testing/     testcontainers helpers and the repository contract suite
scripts/harness/      agent-guide and config lint
web/                  the task_interleaving_mfe Module Federation remote
```

The layering rule is an executable test, not a convention:
`internal/architecture` (run by `make arch-test`, the `arch-test` CI job)
fails the build if the hexagonal or fleet fitness rules are violated.

---

## Domain events

All messages are **CloudEvents 1.0** in structured content mode. The type
convention is `com.warehouse.wes.task-interleaving.<entity>.<EventName>` —
the `wes` subdomain segment, like the rest of the WES tier.

**Publishes** (through the transactional outbox and the always-on relay with
an attempt cap, ADR 0004):

| Topic | Messages | Notes |
|---|---|---|
| `warehouse.task-interleaving.events` | `InterleavingHintIssued` (`com.warehouse.wes.task-interleaving.stationtrip.InterleavingHintIssued`) | The integration topic; key = station id. **Published, not consumed**: `fulfillment-execution`'s `origin/develop` has no code that reads it (its ADRs 0049/0050 list the consumer as out of scope). |
| `warehouse.task-interleaving.analytics` | `InterleavingHintIssued`, `InterleavingHintConsumed`, `InterleavingHintExpired`, `InterleavingPolicyDeclared` (`com.warehouse.wes.task-interleaving.policy.…` for the policy) | The analytics stream (ADR 0008); the last three exist **only** here. The only consumer is this service's own `cmd/ti-projector`. Unappliable messages are dead-lettered byte-for-byte to `warehouse.task-interleaving.analytics.dlq`. |

**Consumes** (each with retry, a `<topic>.dlq` dead-letter topic and
processed-event de-duplication; ADR 0003):

| Topic | Messages | Consumer group |
|---|---|---|
| `warehouse.fulfillment.events` | `TaskAvailable`, `TaskClaimed`, `TaskCompleted` (`com.warehouse.wes.fulfillment-execution.task.*`) — `TaskAvailable` only for RELOCATE and REPLENISH tasks (producer ADR 0050); every other type ignored | `FULFILLMENT_CONSUMER_GROUP` |
| `warehouse.facility.events` | `ZoneRegistered`, `LocationSlotRegistered`, `LocationSlotDecommissioned`, `LocationGeometryUpdated` (`com.warehouse.wms.facility-layout.…`) — the local geometry copy; every other type ignored | `LAYOUT_CONSUMER_GROUP` |

Contracts are pinned in `apis/asyncapi.yaml` (consumer-first, ADR 0003).

---

## Running it

Postgres and Kafka are the only runtime (ADR 0006): there is no in-memory
mode, and `cmd/api` refuses to boot without `DATABASE_URL`, `KAFKA_BROKERS`,
`FULFILLMENT_CONSUMER_GROUP` and `LAYOUT_CONSUMER_GROUP`. The broker
auto-creates topics on first write; the service never calls an admin API.
Migrations run at boot.

```sh
make run-local     # docker compose up Postgres + Kafka (+ a job that pre-creates the four topics), then go run ./cmd/api
make local-down    # stop them (the Postgres volume is kept)
```

Host ports 5432 and 9092 must be free. **The compose file is not verified
end to end** (the development host's shared kind cluster holds host port
9092); the proof that the service runs on real Postgres and Kafka is the
testcontainers end-to-end test in `make integration`, which does not use it.

Or by hand against infrastructure you already have:

```sh
go build -o /tmp/ti-api ./cmd/api
HTTP_ADDR=:18080 DATABASE_URL='postgres://...' KAFKA_BROKERS=broker:9092 \
  FULFILLMENT_CONSUMER_GROUP=ti-fulfillment LAYOUT_CONSUMER_GROUP=ti-layout \
  /tmp/ti-api
```

### Configuration

`cmd/api`:

| Env var | Default | Meaning |
|---|---|---|
| `HTTP_ADDR` | `:8080` | REST listen address |
| `DATABASE_URL` | **required** | Postgres DSN; boot error when unset |
| `KAFKA_BROKERS` | **required** | Comma-separated broker list |
| `FULFILLMENT_CONSUMER_GROUP` | **required** | Stable consumer group for `warehouse.fulfillment.events` |
| `LAYOUT_CONSUMER_GROUP` | **required** | Stable consumer group for `warehouse.facility.events` |
| `FULFILLMENT_MODE` / `LAYOUT_MODE` | *(unset = `kafka`)* | Only `kafka` is accepted; `permissive` is rejected at boot |
| `HINT_EXPIRY_INTERVAL` | `5s` | Hint-expiry worker sweep interval; a bad value is a **boot error**, not a silent default |
| `OUTBOX_RELAY_INTERVAL` | `1s` | Outbox relay sleep between empty passes; malformed/non-positive values warn and use the default |
| `OUTBOX_MAX_ATTEMPTS` | `20` | Outbox relay attempt cap (`0` = unlimited) |
| `SHUTDOWN_DRAIN_DELAY` | `5s` | Graceful-shutdown drain after `/readyz` flips not-ready (`0` disables) |
| `MIGRATIONS_DATABASE_URL` | *(unset = `DATABASE_URL`)* | DSN the boot migrations run against |
| `LOG_LEVEL` | `info` | `debug`/`info`/`warn`/`error`, case-insensitive |

The other binaries:

| Binary | Env vars |
|---|---|
| `cmd/mcp` | `MCP_ADDR` (`:8090`), `DATABASE_URL` (**required**), `MIGRATIONS_DATABASE_URL` (default `DATABASE_URL`), `LOG_LEVEL` |
| `cmd/a2a` | `A2A_ADDR` (`:8093`), `A2A_AGENT_URL` (**required**, absolute URL advertised on the Agent Card), `DATABASE_URL` (**required**), `MIGRATIONS_DATABASE_URL` (default `DATABASE_URL`), `LOG_LEVEL` |
| `cmd/ti-projector` | `ANALYTICS_DATABASE_URL` (**required**), `KAFKA_BROKERS` (**required**), `ANALYTICS_CONSUMER_GROUP` (default `task-interleaving-analytics`), `ADMIN_ADDR` (`:8091`), `LOG_LEVEL` |
| `cmd/ti-reports` | `ANALYTICS_DATABASE_URL` (**required**), `HTTP_ADDR` (`:8092`), `LOG_LEVEL` |

Every variable is also tabulated in
[Configuration](docs/docs/operations/configuration.md); first calls are in
the [Quickstart](docs/docs/overview/quickstart.md).

---

## Observability

Today the service emits **structured logs and nothing else** (see
[Observability](docs/docs/operations/observability.md), which proves the
absence): no OpenTelemetry instrument, span, OTLP exporter or `/metrics`
endpoint exists in `cmd/` or under `internal/` — `go.opentelemetry.io/otel`
appears only as an indirect dependency. What exists:

- **Logs**: JSON (`log/slog`) on stdout, one object per line with `time`,
  `level`, `msg` and structured fields (`addr`, `consumer`, `topic`,
  `group_id`, `interval`, `max_attempts`, …). Level from `LOG_LEVEL`
  (default `info`). There is no HTTP access-log middleware, so a successful
  request leaves no line.
- **Trace-context plumb-through**: the outbox inserts the W3C `traceparent`
  (and `tracestate`/`baggage` when present) of the writing request into the
  row's headers, so a relay copy carries it onto the Kafka message. With no
  tracer provider installed today, no trace header is ever written; an
  integration test installs a propagator itself to prove the mechanism.

---

## API

REST on `HTTP_ADDR` (default `:8080`), specified in `apis/openapi.yaml`.
Errors are **RFC 7807** `application/problem+json` throughout. Ten
operations over nine paths:

| Method | Path | Purpose |
|---|---|---|
| `GET` / `PUT` | `/sites/{siteId}/policy` | Read / declare the site's interleaving policy |
| `GET` | `/hints` | List hints |
| `GET` | `/hints/{hintId}` | Get one hint |
| `GET` | `/stations/{stationId}` | A station's trip state |
| `POST` | `/recommendations` | Rank the site's open tasks for a station that just completed one; issues the hint. **Requires an `Idempotency-Key` header** (a retry with the same key and body replays the original response instead of double-issuing) |
| `GET` | `/healthz` | Liveness |
| `GET` | `/readyz` | Readiness — ready only while Postgres answers **and** a Kafka broker answers a metadata request (topics are not checked) |
| `GET` | `/reports/hint-adoption` | Analytics report — served by `cmd/ti-reports`, not `cmd/api` |
| `GET` | `/reports/freshness` | Analytics freshness — served by `cmd/ti-reports` |

REST problem types are catalogued in
[Troubleshooting](docs/docs/operations/troubleshooting.md).

### Analytics data product (Hint Adoption)

ADR 0008. The OLTP `cmd/api` fans its events out (same outbox transaction) to
both the integration topic and `warehouse.task-interleaving.analytics`;
`cmd/ti-projector` is the **only writer** of the separate analytical database
(consumer group `ANALYTICS_CONSUMER_GROUP`, admin/health on `ADMIN_ADDR`
`:8091`, dead-letters unappliable messages to
`warehouse.task-interleaving.analytics.dlq`); `cmd/ti-reports` serves the
read-only report side on `:8092` (`GET /reports/hint-adoption`,
`GET /reports/freshness`).

---

## Helm chart

`charts/task-interleaving` renders the mandatory Postgres + Kafka runtime and
the optional components — `mcp`, `a2a`, `analytics` (projector + reports) and
`frontend` — **all off by default**. The root `Dockerfile` builds every
`cmd/*` binary into one image; `web/Dockerfile` builds the frontend image.
Chart render assertions live in `charts/task-interleaving/tests/`:
`render_test.sh` (the chart deploys the real runtime — no silent default for
mandatory config, no `*_MODE` variable is ever rendered, and the env the
binary reads is the env the chart renders), `test_service_selectors.py` and
`test_kong_config.py`; CI runs them in the `chart-tests` job.

### Running the MCP server in Kubernetes

`mcp.enabled=true` renders a separate MCP Deployment + Service on port
`8090`. The server (ADR 0007) is **read-only** — four tools
(`get_site_policy`, `list_hints`, `get_hint`, `get_station`) over the
existing queries, no write tool, no Kafka — served over Streamable HTTP at
`/mcp`. `GET /healthz` backs the probes; there is no authentication layer in
front of it.

### Running the A2A agent in Kubernetes

`a2a.enabled=true` renders the A2A Deployment + Service on port `8093`
(ADR 0009). Read-only JSON-RPC over HTTP at `/` and `/a2a`, Agent Card at
`/.well-known/agent-card.json`, two goal-shaped read skills
(`station_hint_status`, `site_interleaving_overview`) over the same read use
cases as REST and MCP; no write skill, no Kafka. `A2A_AGENT_URL` is
mandatory.

---

## Operator micro-frontend (`web/`)

`web/` is `task_interleaving_mfe`, this context's Module Federation remote
(hints, stations, site policy with `If-Match`), talking only to this
service's own REST API (`/api/task-interleaving`) and never part of
`make check`.

```bash
cd web && npm install && npm run dev     # http://localhost:5197
```

Deployed, it is built into a static bundle served by its own nginx image
(`frontend.enabled=true`); the web gateway serves it at
`http://localhost/mfes/task-interleaving/`. The remote reads its API origin
at runtime from `window.__WAREHOUSE_CONFIG__.apiOrigin`
(`${apiOrigin}/api/task-interleaving`); a production build with no runtime
config fails loudly instead of guessing. Not yet wired into the cluster's
`warehouse-infra` entry, and the shell's entry for the remote in
`warehouse-console` is not built yet — the
[Integration](docs/docs/ecosystem/integration.md) page tracks the exact
state of each edge.

---

## Local development / quality gate

| Command | What it runs |
|---|---|
| `make check-fast` | quickest gate: `fmt-check`, `vet`, `arch-test` |
| `make check` | gofmt check, `vet`, build, golangci-lint v2.14.0, `go test -race` — needs no database |
| `make check-all` | `check` plus the 90% coverage gate, architecture tests and BDD — run this before pushing |
| `make coverage` | the CI coverage command + the 90% gate |
| `make bdd` | the godog suite (`go test ./... -run TestFeatures -v`) |
| `make integration` | testcontainers suites, needs Docker (`go test -tags=integration ./... -race -count=1`), including the end-to-end test that boots `run()` on real Postgres and Kafka |
| `make arch-test` | the fitness tests in `internal/architecture` |
| `make mutation` | gremlins on the four domain packages, thresholds in `.gremlins.yaml` |
| `make vuln` | `govulncheck ./...` |
| `make run-local` / `make local-down` | the service on a docker-compose Postgres + Kafka / stop them |

Git hooks are managed by [lefthook](https://github.com/evilmartians/lefthook)
(`lefthook.yml`; pre-commit: `fmt-check` + `vet` + `lint`; pre-push:
`make check`). Activate once per clone: `brew install lefthook && lefthook install`.

## Tests

- **Unit**: `go test -race ./...`; domain + application coverage gate **≥ 90%**
  (`make coverage`, CI `test` job).
- **BDD**: the godog suite — 6 `.feature` files, 68 scenarios over the real
  HTTP API (`make bdd`, CI `bdd` job).
- **Integration**: testcontainers Postgres/Kafka suites, including an
  end-to-end test that boots `run()` on real Postgres and Kafka
  (`make integration`, CI `integration` job).
- **Architecture fitness**: `internal/architecture` (`make arch-test`, CI
  `arch-test` job).
- **Mutation**: gremlins over `internal/domain` with blocking thresholds in
  `.gremlins.yaml` — `mutation-fast` on every push/PR, the exhaustive
  `mutation` run weekly (schedule).

CI (`.github/workflows/ci.yml`) runs on every push and PR: `lint`,
`guide-lint` (agent-guide lint, `scripts/harness/`), `complexity`, `test`,
`bdd`, `integration`, `mutation-fast`, `api-lint`, `vuln` (govulncheck),
`chart-tests`, `arch-test`, `web` (the `web/` remote), plus the scheduled
`mutation` and `drift` sensors; `docker-publish` pushes the image and
`release` tags and publishes the chart. Every sensor is also a `make`
target — `make help` lists them all.

## Architecture Decision Records

The ADR index lives at [docs/adr/index.md](docs/adr/index.md) — 9 decisions
(0001–0009), from the bounded context to the A2A inbound adapter.

## License

MIT — see [LICENSE](LICENSE).
