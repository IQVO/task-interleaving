# task-interleaving

Task Interleaving: WES-tier Supporting bounded context of the warehouse-systems fleet. When a station completes a task it ranks the open tasks of the site by travel distance and issues an advisory hint, so operators chain compatible work instead of travelling empty. fulfillment-execution stays pull-based and uses the hint only as a tie-break inside one priority band. Hexagonal Go, Postgres, REST, Kafka (CloudEvents 1.0).

- Design and pinned contracts: `docs/adr/` and the fleet plan `.hermes/plans/2026-10-17_005500-pick-face-replenishment-and-task-interleaving.md`.
- Scaffolded from `warehouse-harness-template` (v3). Guides: `.claude/rules/`, sensors: `HARNESS.md`.
- Commands: `make check-fast`, `make check-all`, `make integration`.

## Status

The service is being built on a separate branch and is not on `develop` yet. What `develop` has today:

- the domain: `internal/domain/policy` (InterleavingPolicy), `travel` (the distance port and `rectilinear-v1`), `recommend` (the pure ranker) and `stationtrip` (StationTrip with its hint lifecycle), at 100% statement coverage and a 90/90 mutation gate;
- the contracts: `apis/openapi.yaml` (REST) and `apis/asyncapi.yaml` (events);
- the architecture fitness tests in `internal/architecture`;
- the CloudEvents helper (`internal/adapters/kafka/cloudevents`) and the acks=all Kafka writer constructor (`internal/adapters/outbound/kafka`);
- ADRs 0001 to 0004 in `docs/adr/`.

There is no `cmd/`, no migration, no HTTP server, no Kafka consumer and no outbox relay on `develop`, so there is nothing to start yet.

## Layout

```text
apis/                 openapi.yaml, asyncapi.yaml (the contracts)
docs/                 Docusaurus site (docs/docs) and the ADRs (docs/adr)
internal/domain/      policy, travel, recommend, stationtrip
internal/adapters/    kafka/cloudevents, outbound/kafka
internal/architecture hexagonal and fleet fitness tests
scripts/harness/      agent-guide and config lint
```

## Quality gates

| Command | What it runs |
| --- | --- |
| `make check` | gofmt check, vet, build, golangci-lint v2.13.1, `go test -race` |
| `make check-all` | `check` plus the 90% coverage gate, architecture tests and BDD |
| `make arch-test` | the fitness tests in `internal/architecture` |
| `make mutation` | gremlins on the four domain packages, thresholds in `.gremlins.yaml` |

CI is `.github/workflows/ci.yml`; the jobs are listed in the Testing page below.

## Documentation

Published at <https://iqvo.github.io/task-interleaving/> (built from `docs/` by `.github/workflows/docs.yml`; pull requests build it, `main` deploys it). The service itself is not on `develop` yet: the pages below describe the domain, the contracts and the tests that are, and mark everything else as designed or pending.

| Page | Source file | What it covers |
| --- | --- | --- |
| Introduction | [`docs/docs/overview/introduction.md`](docs/docs/overview/introduction.md) | What the context owns, classification, capabilities, what is on `develop` |
| Architecture | [`docs/docs/overview/architecture.md`](docs/docs/overview/architecture.md) | Layers, the `travel.Model` port, component diagram, fitness rules |
| Quickstart | [`docs/docs/overview/quickstart.md`](docs/docs/overview/quickstart.md) | `make` targets and a verified domain walk-through (no server to run yet) |
| Testing | [`docs/docs/development/testing.md`](docs/docs/development/testing.md) | Test pyramid as it is, mutation thresholds, every CI job |
| Integration | [`docs/docs/ecosystem/integration.md`](docs/docs/ecosystem/integration.md) | Upstream and downstream contracts with status |
| Use cases | [`docs/docs/ddd/use-cases.md`](docs/docs/ddd/use-cases.md) | Domain operations, invariants, events (application layer pending) |
| Subdomain classification | [`docs/docs/ddd/subdomain-classification.md`](docs/docs/ddd/subdomain-classification.md) | Supporting, WES tier, neighbours from the fleet table |
| DDD artifacts | [`docs/docs/ddd/ddd-artifacts.md`](docs/docs/ddd/ddd-artifacts.md) | Index of the 11-page ddd-crew pack (core domain chart, canvas, context map, aggregate canvas, message flow, EventStorming, language, class, ER, sequence, events) |
| API reference | [`docs/docs/api-reference/overview.md`](docs/docs/api-reference/overview.md), [`events.md`](docs/docs/api-reference/events.md) | REST (generated from `apis/openapi.yaml`) and event catalogue |
| ADRs | [`docs/adr/index.md`](docs/adr/index.md) | Decisions 0001 to 0004 with status |

Not written yet because there is no code to ground them: configuration, runbook, observability and troubleshooting (operations), and an MCP page. They arrive with the service.

Build the site locally: `cd docs && npm ci && npm run build`.
