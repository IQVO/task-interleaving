# task-interleaving

Task Interleaving: WES-tier Supporting bounded context of the warehouse-systems fleet. When a station completes a task it ranks the open tasks of the site by travel distance and issues an advisory hint, so operators chain compatible work instead of travelling empty. fulfillment-execution stays pull-based and uses the hint only as a tie-break inside one priority band. Hexagonal Go, Postgres, REST, Kafka (CloudEvents 1.0).

- Design and pinned contracts: `docs/adr/` and the fleet plan `.hermes/plans/2026-10-17_005500-pick-face-replenishment-and-task-interleaving.md`.
- Scaffolded from `warehouse-harness-template` (v3). Guides: `.claude/rules/`, sensors: `HARNESS.md`.
- Commands: `make check-fast`, `make check-all`, `make integration`.

## Status

The service is on `develop` (merged in PR #7). One binary, `cmd/api`:

- REST on `HTTP_ADDR` (default `:8080`) for `apis/openapi.yaml`, RFC 7807 problems, an `Idempotency-Key` middleware on `POST /recommendations`, `/healthz` and `/readyz`;
- Postgres repositories with embedded boot migrations (Postgres is mandatory: there is no in-memory mode, [ADR 0006](docs/adr/0006-postgres-and-kafka-are-the-only-runtime.md));
- a transactional outbox and a Kafka relay with an attempt cap, always on;
- a hint-expiry worker;
- two Kafka consumers (`FULFILLMENT_MODE`, `LAYOUT_MODE`, always on; `permissive` is rejected at boot) with retry, a `<topic>.dlq` dead-letter topic and processed-event de-duplication;
- `/readyz` that is ready only while Postgres answers and every topic the service consumes or produces has a Kafka leader.

Packaged: a `Dockerfile` and a Helm chart (`charts/task-interleaving`) that render the Postgres + Kafka runtime. Not built: an MCP server, OpenTelemetry metrics or traces, a `warehouse-infra` entry, and a consumer of the hint in fulfillment-execution. The Runbook and Integration pages give the exact state of each edge.

## Run it

The service needs Postgres and Kafka and refuses to boot without `DATABASE_URL`, `KAFKA_BROKERS`, `FULFILLMENT_CONSUMER_GROUP` and `LAYOUT_CONSUMER_GROUP`. `make run-local` brings both up with `docker-compose.yml` (a job creates the four topics: the service never creates topics) and runs the service on them; host ports 5432 and 9092 must be free. **Not verified end to end:** the compose file could not be exercised on the development host, where the shared kind cluster already holds host port 9092. The proof that the service runs on real Postgres and Kafka is the testcontainers end-to-end test (`make integration`), which does not use this file:

```bash
make run-local     # docker compose up Postgres + Kafka + topics, then go run ./cmd/api
make local-down    # stop them (the Postgres volume is kept)
```

Or by hand against infrastructure you already have:

```bash
go build -o /tmp/ti-api ./cmd/api
HTTP_ADDR=:18080 DATABASE_URL='postgres://...' KAFKA_BROKERS=broker:9092 \
  FULFILLMENT_CONSUMER_GROUP=ti-fulfillment LAYOUT_CONSUMER_GROUP=ti-layout \
  /tmp/ti-api        # migrations run at boot
```

Every variable is on [Configuration](docs/docs/operations/configuration.md); the first calls are in the [Quickstart](docs/docs/overview/quickstart.md).

## Layout

```text
apis/                 openapi.yaml, asyncapi.yaml (the contracts)
charts/task-interleaving/  Helm chart (values, templates, tests/), plus the root Dockerfile
cmd/api/              the composition root (config, adapters, consumers, relay, topics)
migrations/           embedded SQL migrations (0001_interleaving_schema)
features/             godog scenarios (6 .feature files, 68 scenarios) and their runner and steps (package features_test); the repo root holds no .go file
docs/                 Docusaurus site (docs/docs) and the ADRs (docs/adr)
internal/domain/      policy, travel, recommend, stationtrip
internal/application/ usecases, ports, repository, outbox
internal/adapters/    inbound/{http,kafka,expiry}, outbound/{postgres,memory,events,outbox,kafka,system}, kafka/cloudevents
internal/architecture hexagonal and fleet fitness tests
internal/testing/     testcontainers helpers and the repository contract suite
scripts/harness/      agent-guide and config lint
```

## Quality gates

| Command | What it runs |
| --- | --- |
| `make check` | gofmt check, vet, build, golangci-lint v2.14.0, `go test -race` |
| `make check-all` | `check` plus the 90% coverage gate, architecture tests and BDD |
| `make bdd` | the godog suite (`go test ./... -run TestFeatures -v`) |
| `make integration` | testcontainers suites, needs Docker (`go test -tags=integration ./... -race -count=1`), including the end-to-end test that boots `run()` on real Postgres and Kafka |
| `make run-local` / `make local-down` | the service on a docker-compose Postgres + Kafka / stop them |
| `make arch-test` | the fitness tests in `internal/architecture` |
| `make mutation` | gremlins on the four domain packages, thresholds in `.gremlins.yaml` |

CI is `.github/workflows/ci.yml`; the jobs are listed in the Testing page below.

## Documentation

Published at <https://iqvo.github.io/task-interleaving/> (built from `docs/` by `.github/workflows/docs.yml`; pull requests build it, `main` deploys it).

| Page | Source file | What it covers |
| --- | --- | --- |
| Introduction | [`docs/docs/overview/introduction.md`](docs/docs/overview/introduction.md) | What the context owns, classification, capabilities, what is on `develop` |
| Architecture | [`docs/docs/overview/architecture.md`](docs/docs/overview/architecture.md) | Layers, the binary and its port, container diagram, data stores, fitness rules |
| Quickstart | [`docs/docs/overview/quickstart.md`](docs/docs/overview/quickstart.md) | Build, run on Postgres and Kafka, first calls with real output, the end-to-end proof |
| Configuration | [`docs/docs/operations/configuration.md`](docs/docs/operations/configuration.md) | Every environment variable of `cmd/api`, the mandatory ones, compiled-in settings |
| Runbook | [`docs/docs/operations/runbook.md`](docs/docs/operations/runbook.md) | Deployment facts, probes, boot and shutdown order, migrations, Kafka topics and groups, outbox relay, expiry worker, procedures |
| Observability | [`docs/docs/operations/observability.md`](docs/docs/operations/observability.md) | Logs and their fields; the absence of metrics and traces; suggested checks |
| Troubleshooting | [`docs/docs/operations/troubleshooting.md`](docs/docs/operations/troubleshooting.md) | Symptom, cause, check, fix; REST problem types |
| Testing | [`docs/docs/development/testing.md`](docs/docs/development/testing.md) | Test pyramid as it is, make targets, BDD and integration suites, mutation thresholds, every CI job |
| Integration | [`docs/docs/ecosystem/integration.md`](docs/docs/ecosystem/integration.md) | Upstream and downstream contracts with the status of every edge |
| Use cases | [`docs/docs/ddd/use-cases.md`](docs/docs/ddd/use-cases.md) | The 14 application use cases: trigger, inputs, invariants, events |
| Subdomain classification | [`docs/docs/ddd/subdomain-classification.md`](docs/docs/ddd/subdomain-classification.md) | Supporting, WES tier, neighbours from the fleet table |
| DDD artifacts | [`docs/docs/ddd/ddd-artifacts.md`](docs/docs/ddd/ddd-artifacts.md) | Index of the 11-page ddd-crew pack (core domain chart, canvas, context map, aggregate canvas, message flow, EventStorming, language, class, ER, sequence, events) |
| API reference | [`docs/docs/api-reference/overview.md`](docs/docs/api-reference/overview.md), [`events.md`](docs/docs/api-reference/events.md) | REST (generated from `apis/openapi.yaml`) and event catalogue |
| ADRs | [`docs/adr/index.md`](docs/adr/index.md) | Decisions 0001 to 0006 with status |

There is no MCP page because there is no `cmd/mcp`.

Build the site locally: `cd docs && npm ci && npm run build`.
