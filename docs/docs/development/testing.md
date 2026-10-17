---
id: testing
title: Testing
sidebar_label: Testing
sidebar_position: 1
---

# Testing

The test pyramid **as it is on `develop`**: a pure domain under a mutation gate,
use cases and adapters with unit and httptest tests, a godog BDD suite over the
real use cases, testcontainers integration suites against real Postgres and
Kafka, architecture fitness tests, and spec linting.

| Layer | Count / threshold | Command |
| --- | --- | --- |
| Unit, domain | 85 top-level test functions in 4 packages (`policy` 16, `recommend` 17, `stationtrip` 43, `travel` 9) | `make test` |
| Unit, application | 24 in `internal/application/{usecases,outbox}` (23 + 1) | `make test` |
| Unit and httptest, adapters and `cmd/api` | `inbound/http` 55, `inbound/kafka` 46, `inbound/expiry` 11, `outbound/memory` 24, `outbound/events` 13, `outbound/outbox` 13, `outbound/kafka` 5, `kafka/cloudevents` 6, `outbound/postgres` 1 (DSN selection), `cmd/api` 25, `bootretry` 6 | `make test` |
| Coverage gate | 90% over `./internal/domain/...,./internal/application/...`; measured 99.9% on this commit | `make coverage` |
| BDD (godog / Gherkin) | 6 feature files, **68 scenarios** (64 `Scenario` blocks, one of which is an outline with 5 example rows), 608 steps; all pass | `make bdd` |
| Integration (testcontainers) | 55 top-level tests behind `//go:build integration`: `cmd/api` 7, `inbound/kafka` 4, `outbound/outbox` 8, `outbound/postgres` 36 | `make integration` |
| Architecture fitness | 22 top-level test functions in `internal/architecture` (one, the MCP optional-inputs sensor, skips because there is no MCP adapter) | `make arch-test` |
| Mutation (gremlins) | efficacy 90, mutant coverage 90 (`.gremlins.yaml`); measured 100% / 100% on the four domain packages | `make mutation`, `make mutation-full` |
| Spec linting (Spectral) | OpenAPI and AsyncAPI, `--fail-severity=warn` | CI `api-lint` |
| Contract (schemathesis) | **none**: no schemathesis configuration and no CI job. Response shapes are checked against the OpenAPI document by a Go test instead (`internal/adapters/inbound/http/shape_test.go`) | none |
| Evals | **none**: `EVALS.md` at the repo root is a copy of the harness template's note about agent evaluation on inventory-storage; it measures nothing in this repo | none |

Counts come from `grep '^func Test'` per package on this commit; many tests are
table-driven, so the number of cases is larger. Run `go test ./... -v` for the
cases.

## Make targets

All targets mirror a CI job; the `Makefile` is the only definition of the coverage
scope and the mutation package list.

| Target | Runs | Notes |
| --- | --- | --- |
| `make help` | prints the target list | default target |
| `make build` | `go build ./...` | compiles `cmd/api` |
| `make vet` | `go vet ./...` | |
| `make fmt` / `make fmt-check` | `gofmt -w .` / fail if `gofmt -l .` is non-empty | |
| `make lint` | `golangci-lint run ./...` | pinned `v2.14.0`, config `.golangci.yml` |
| `make test` | `go test ./... -race` | unit, httptest and BDD (the BDD suite is a normal `Test` function) |
| `make coverage` | `go test ./... -race -coverprofile=coverage.out -coverpkg=./internal/domain/...,./internal/application/...` then the 90% gate | The CI `test` job runs exactly this; a workflow that defined its own `-coverpkg` would fail `repo_lint` R11. |
| `make arch-test` | `go test ./internal/architecture/... -v` | |
| `make bdd` | `go test ./... -run TestFeatures -v` | the godog suite, `features_test.go` at the module root |
| `make integration` | `go test -tags=integration ./... -race -count=1` | needs only Docker; every test boots its own Postgres and Kafka |
| `make mutation` | `gremlins unleash` on each of `./internal/domain/stationtrip ./internal/domain/recommend ./internal/domain/travel ./internal/domain/policy` | the fast, blocking subset (`MUTATION_FAST_PKGS`) |
| `make mutation-full` | `gremlins unleash ./internal/domain --workers 1 --timeout-coefficient 30` | the exhaustive scheduled run |
| `make vuln` | `govulncheck ./...` | |
| `make check` | `fmt-check vet build lint test` | the pre-push hook runs this |
| `make check-all` | `check coverage arch-test bdd` | run before pushing |
| `make check-fast` | `fmt-check vet arch-test` plus `go test` of the packages changed vs `HEAD` | the agent Stop hook |
| `make guide-lint` / `make harness-test` | `scripts/harness/guide_lint.py` / `scripts/harness/test_hook.py` | agent-guide checks, not product tests |

A new package under `internal/domain/` must be added to `MUTATION_FAST_PKGS` **and**
to the `for pkg in ...` loop of the `mutation-fast` CI job; the two lists are kept
in sync by hand.

## Unit tests

- **Domain** (`policy`, `travel`, `recommend`, `stationtrip`): pure, standard
  library only, no fixtures. They pin the site-id token rules, the policy
  boundaries (`MaxDetourM`, `MaxCandidates` 1 to 5, `HintTTL`, `VerticalWeight`),
  the rule *unknown geometry is unknown, never zero*, ranking by detour then task
  id independent of pool order, the claim time window (one nanosecond before
  issue, before expiry, at expiry), one Active hint per station, consumption at
  most once, and ordered domain events.
- **Use cases** (`internal/application/usecases`): run against the in-memory
  adapters, including injected faults (`faults_test.go`), input validation
  (`inputs_test.go`) and the completion path (`completion_station_test.go`).
- **HTTP adapter**: `httptest` over the real router, one file per handler group
  plus `idempotency_test.go` (replay, reuse, rollback on 4xx and 5xx, concurrent
  first writers), `health_test.go` and `shape_test.go`, which walks the OpenAPI
  schema and checks that the responses have exactly the documented shape.
- **Kafka consumers**: a fake `Reader` drives the run loop (`consume_loop_test.go`:
  retry, backoff sequence, dead-letter after 5 attempts, no commit before
  success), `deadletter_test.go`, and per-consumer tests whose fixtures restate
  the producers' published contracts as raw JSON, never built with this
  service's own helper.
- **Outbox relay and events**: relay cap and ordering with a fake store,
  golden CloudEvents bytes in `outbound/events/testdata`.
- **`cmd/api`**: env parsing (`config_test.go`), consumer planning
  (`consumers_test.go`), shutdown order (`shutdown_test.go`), boot (`main_test.go`).

Run one package or one test:

```bash
go test ./internal/domain/recommend -run TestRecommendDetourBoundary -v
go test ./internal/application/usecases -v
```

## BDD (godog)

`features/*.feature` holds six files: `hints.feature` (13 scenarios: hint
lifecycle, supersede, consume, expiry, duplicates), `inert.feature` (3),
`policy.feature` (12 blocks, one outline with 5 rows), `ranking.feature` (16),
`simulation.feature` (10) and `tasks.feature` (10: pool and claim copies).
`features_test.go` and its `features_setup/flow/assertions_test.go` companions
wire the **real use cases over the in-memory adapters** with a fake clock, so the
scenarios are fast and need no Docker. `go test . -run TestFeatures -v` ends with
`68 scenarios (68 passed)` and `608 steps (608 passed)`.

## Integration (testcontainers)

All files carry `//go:build integration` and boot their own containers
(`internal/testing/pgtest`, `internal/testing/kafkatest`); `TestMain` in
`cmd/api`, `outbound/outbox` and `outbound/postgres` starts the shared
Postgres, and a missing Docker is a test failure, never a skip.

| Suite | Package | What it proves |
| --- | --- | --- |
| Service over Postgres | `cmd/api` (7) | boot migrates, `Idempotency-Key` is enforced, the simulation writes no outbox row, policy `PUT` is version guarded, a rejection leaves no effect and replays, concurrent requests with one key store one answer, the expiry worker expires a hint and enqueues the fact, boot **fails closed** on an unreachable database |
| Repositories and contract | `outbound/postgres` (36) | the repository contract suite (`internal/testing/repocontract`) for policies, trips, task pool, location book, processed events and the idempotency store; one Active hint per station even under racing issuers; version-guarded policy writes under races; unit-of-work rollback and nesting; outbox ordering, retry, attempt cap and trace-context headers; migrations apply, re-run as a no-op, every `down` restores the previous schema, and run from the unpooled URL only |
| Outbox relay | `outbound/outbox` (8) | exact envelopes published for issued/consumed/expired, crash and restart without duplicates, a crash between send and mark republishes the **same id**, the attempt cap excludes a poison row, re-arming claims it again, no cap lets a poison row block later rows |
| Consumers | `inbound/kafka` (4) | the fulfillment consumer and the layout consumer over a real Kafka and Postgres, and the no-policy case issuing no hint |

`make integration` and the CI `integration` job run the same command,
`go test -tags=integration ./... -race -count=1`, which covers every package, so
all four suites run in CI. An architecture fitness test fails the
build if an integration test gates on `DATABASE_URL`/`KAFKA_BROKERS` or
hard-codes `localhost:9092`.

## Mutation testing

`.gremlins.yaml` sets `workers: 1`, `timeout-coefficient: 30`, and the thresholds
`efficacy: 90` and `mutant-coverage: 90`. Gremlins fails when the measured value is
**less than or equal to** the threshold, so a threshold sits strictly below what the
code achieves. The file records a measurement of 2026-10-17 on the four domain
packages (`stationtrip`, `recommend`, `travel`, `policy`): efficacy 100% and
mutator coverage 100% in each, with 0 lived mutants. Only the domain is mutated;
use cases and adapters are not.

The scheduled `mutation` job (Monday 06:00 UTC and on manual dispatch) mutates
`./internal/domain` as a whole. The `drift` job additionally runs
`scripts/coverage-quality.py` to flag functions with high line coverage and a
surviving mutant. A failing scheduled run opens or refreshes a `harness:red` issue
(`scripts/harness/red_issue.py`).

## Architecture fitness tests

`internal/architecture` carries the dependency-direction rules and source-scanning
sensors listed with their rules on [Architecture](/docs/overview/architecture#rules-enforced-by-internalarchitecture).
Each sensor that scans source also has a test that proves it can fail
(`...SensorBites`, `...SensorFailsOnBadFixtures`, `TestEventCatalogueDetector`).
Only `TestMCPOptionalToolInputsCarryOmitempty` skips (there is no MCP adapter);
`go test` prints the reason.

```bash
make arch-test
```

## CI jobs

`.github/workflows/ci.yml` runs on push and pull request to `main` and `develop`,
weekly (Monday 06:00 UTC, mutation and drift only) and on manual dispatch.

| Job | Runs | When |
| --- | --- | --- |
| `lint` | `golangci-lint` v2.14.0, `--timeout=5m` | always |
| `guide-lint` | `scripts/harness/guide_lint.py` and `scripts/harness/test_hook.py`; **advisory** (`continue-on-error`) | always |
| `complexity` | golangci-lint with only `gocyclo,gocognit,cyclop,funlen,nestif` (gate 15) plus an informational gocyclo report | always |
| `test` | `go build ./...`, `go vet ./...`, then `make coverage` (90% gate); uploads `coverage.out` | always |
| `bdd` | `go test ./... -run TestFeatures -v` | always |
| `integration` | `go test -tags=integration ./... -race -count=1` (testcontainers boots Postgres and Kafka on the runner's Docker) | always |
| `mutation-fast` | installs gremlins v0.6.0, then `gremlins unleash` on `stationtrip`, `recommend`, `travel`, `policy` | always; blocking |
| `mutation` | `gremlins unleash ./internal/domain --workers 1 --timeout-coefficient 30`, harness:red issue on failure | schedule and dispatch only |
| `drift` | deadcode, `go mod tidy -diff`, knip in `web/`, coverage-quality; advisory | schedule and dispatch only |
| `api-lint` | Spectral on `apis/openapi.yaml` (`.spectral.yaml`) and `apis/asyncapi.yaml` (`.spectral.asyncapi.yaml`), `--fail-severity=warn` | always |
| `vuln` | `govulncheck ./...` | always |
| `chart-tests` | every `charts/task-interleaving/tests/*.sh` and `test_*.py`; with none it prints a warning and exits 0 | always |
| `arch-test` | `go test ./internal/architecture/... -v` | always |
| `docker-publish` | build, push to `ghcr.io/iqvo/task-interleaving`, cosign signature, SBOM; needs the jobs above | push to `main` only |
| `release` | tags the next patch version, retags the image, packages and pushes the Helm chart, creates the GitHub release | push to `main` only, after `docker-publish` |

`.github/workflows/docs.yml` builds this site (`npm ci`, `npm run build` in `docs/`)
on pull requests that touch `docs/**`, `apis/**` or the workflow, and deploys to
GitHub Pages only on push to `main`. `.github/workflows/ai-review.yml` is an
advisory architecture review on pull requests into `develop`; it does nothing
without the `ANTHROPIC_API_KEY` secret.

Jobs that show *skipping* on a pull request into `develop` (`mutation`, `drift`,
`docker-publish`, `release`) are gated on events or branches by their `if:`
conditions; that is expected, not a failure.

### What the pipeline assumes that develop does not have

These are mismatches between `ci.yml` and the tree, reported so nobody reads a green
check as more than it is:

- `docker-publish` builds with `context: .`, but there is no `Dockerfile`; the first
  push to `main` would fail there.
- `release` and `chart-tests` refer to `charts/task-interleaving`, which does not
  exist (`chart-tests` therefore passes with a warning).
- The `drift` job runs `npm ci` in `web/`, which does not exist.
- `HARNESS.md` lists `docs-api-drift`, `helm-lint`, `trivy-scan` and CodeQL as
  sensors; none is in `ci.yml`. There is no job that regenerates the REST
  reference and fails on a diff, so regenerate it yourself after any spec change.

## Reproducing the docs build

```bash
cd docs
npm ci
npx tsc --noEmit -p .
npm run build
```

The build fails on a broken link or anchor (`onBrokenLinks` and `onBrokenAnchors`
are `throw`). Mermaid diagrams are checked with `scripts/validate-mermaid.cjs` of the
warehouse-docs repository, because `docusaurus build` alone never catches a broken
diagram.
