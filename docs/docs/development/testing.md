---
id: testing
title: Testing
sidebar_label: Testing
sidebar_position: 1
---

# Testing

This page describes the test pyramid **as it is on `develop`**: a pure domain with
exhaustive unit tests under a mutation gate, architecture fitness tests, two small
adapter tests and spec linting. The upper layers of the fleet pyramid (BDD,
integration, contract) have no tests here yet because the code they exercise (the
service) is not merged.

| Layer | Present on develop | Count / threshold | Command |
| --- | --- | --- | --- |
| Unit, domain | yes | 77 top-level test functions in 4 packages, 100% statement coverage, 90% gate | `make test`, `make coverage` |
| Unit, adapters | yes | 6 tests in `internal/adapters/kafka/cloudevents`, 1 in `internal/adapters/outbound/kafka` | `make test` |
| Architecture fitness | yes | 23 top-level test functions in `internal/architecture` | `make arch-test` |
| Mutation (gremlins) | yes | efficacy 90, mutant coverage 90 (`.gremlins.yaml`); measured 100% / 100% | `make mutation`, `make mutation-full` |
| Spec linting (Spectral) | yes | OpenAPI and AsyncAPI, `--fail-severity=warn` | CI `api-lint` |
| BDD (godog / Gherkin) | **no** | 0 `.feature` files, 0 scenarios, no godog dependency | `make bdd` passes vacuously |
| Integration (testcontainers) | **no** | no file carries the `integration` build tag | `make integration` runs `go test -tags=integration ./... -race -count=1` over nothing |
| Contract (schemathesis) | **no** | no schemathesis configuration, no server to call | none |
| Evals | **no** | `EVALS.md` at the repo root is a copy of the harness template's note about agent evaluation on inventory-storage; it measures nothing in this repo | none |

Counts come from `grep -c '^func Test'` per package on this commit
(domain: policy 16, recommend 17, stationtrip 35, travel 9; adapters 6 and 1;
architecture 23; total 107) and many of them are table-driven, so the number of
cases is larger. Run `go test ./... -v` for the cases.

## Make targets

All targets mirror a CI job; the `Makefile` is the only definition of the coverage
scope and the mutation package list.

| Target | Runs | Notes |
| --- | --- | --- |
| `make help` | prints the target list | default target |
| `make build` | `go build ./...` | |
| `make vet` | `go vet ./...` | |
| `make fmt` / `make fmt-check` | `gofmt -w .` / fail if `gofmt -l .` is non-empty | |
| `make lint` | `golangci-lint run ./...` | pinned `v2.13.1`, config `.golangci.yml` |
| `make test` | `go test ./... -race` | |
| `make coverage` | `go test ./... -race -coverprofile=coverage.out -coverpkg=./internal/domain/...,./internal/application/...` then the 90% gate | The CI `test` job runs exactly this; a workflow that defined its own `-coverpkg` would fail `repo_lint` R11. |
| `make arch-test` | `go test ./internal/architecture/... -v` | |
| `make bdd` | `go test ./... -run TestFeatures -v` | vacuous today: nothing is named `TestFeatures` |
| `make integration` | `go test -tags=integration ./... -race -count=1` | needs Docker when a test exists; none does |
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

## Unit tests of the domain

The four domain packages are pure (standard library only, plus each other), so the
tests need no fixtures and no container:

- `policy`: site id token rules (rune count, space, `/`, control characters),
  boundaries of `MaxDetourM` (NaN, ±Inf, 0, just above 0), `MaxCandidates` 1 to 5,
  `HintTTL`, `VerticalWeight`, the eligible-type set (sorted, de-duplicated, unknown
  and lower-case refused), immutability through accessors, `WithChanges` bumping the
  version, `Restore` validating a persisted row.
- `travel`: every axis with its weight, symmetry, rounding to 0.1 m (half rounds
  up), and the rule *unknown geometry is unknown, never zero* for a missing point and
  for NaN and Inf in each coordinate.
- `recommend`: ranking by detour then task id, the tie-break being the id and not the
  pool order, the cut to `MaxCandidates`, the detour boundary (equal to the limit
  qualifies), each exclusion rule on its own (other site, ineligible type, the
  completed task, no geometry, too far), de-duplication keeping the smallest detour,
  independence of pool order, no mutation of the input, `ErrOriginUnknown` and
  `ErrNoModel`.
- `stationtrip`: position moving forward only (older and equal instants ignored,
  validation before ignoring), one Active hint across many issues, supersede but not
  of a terminal hint, the candidate rules (count, duplicates, eligibility, detour,
  rank 1..n, ordering), the claim time window (at issue, one nanosecond before issue,
  one nanosecond before expiry, at expiry), consumption at most once, expiry at and
  after `expires_at`, ordered and clearable domain events, copies returned by
  accessors, and a full trip (`TestAFullTrip`).

Run one package or one test:

```bash
go test ./internal/domain/recommend -run TestRecommendDetourBoundary -v
go test ./internal/domain/... -cover
```

## Mutation testing

`.gremlins.yaml` sets `workers: 1`, `timeout-coefficient: 30`, and the thresholds
`efficacy: 90` and `mutant-coverage: 90`. Gremlins fails when the measured value is
**less than or equal to** the threshold, so a threshold sits strictly below what the
code achieves. A run of `make mutation` on this commit measured:

| Package | Killed | Lived | Not covered | Efficacy | Mutator coverage | Duration |
| --- | --- | --- | --- | --- | --- | --- |
| `stationtrip` | 51 | 0 | 0 | 100% | 100% | 2 min 1 s |
| `recommend` | 7 | 0 | 0 | 100% | 100% | 1 min 7 s |
| `travel` | 14 | 0 | 0 | 100% | 100% | 40 s |
| `policy` | 26 | 0 | 0 | 100% | 100% | 41 s |

The scheduled `mutation` job (Monday 06:00 UTC and on manual dispatch) mutates
`./internal/domain` as a whole. The `drift` job additionally runs
`scripts/coverage-quality.py` to flag functions with high line coverage and a
surviving mutant. A failing scheduled run opens or refreshes a `harness:red` issue
(`scripts/harness/red_issue.py`).

## Architecture fitness tests

`internal/architecture` carries the dependency-direction rules and source-scanning
sensors listed with their rules on [Architecture](/docs/overview/architecture#rules-enforced-by-internalarchitecture).
Each sensor that scans source also has a test that proves it can fail
(`...SensorBites`, `...SensorFailsOnBadFixtures`, `TestEventCatalogueDetector`). Three
sensors skip today because the code they police does not exist
(`TestDeadLetterCallsGuardCancellation`, `TestMCPOptionalToolInputsCarryOmitempty`,
`TestOutboxRelaysPassAttemptCap`); `go test` prints the reason of each skip.

```bash
make arch-test
```

## CI jobs

`.github/workflows/ci.yml` runs on push and pull request to `main` and `develop`,
weekly (Monday 06:00 UTC, mutation and drift only) and on manual dispatch.

| Job | Runs | When |
| --- | --- | --- |
| `lint` | `golangci-lint` v2.13.1, `--timeout=5m` | always |
| `guide-lint` | `scripts/harness/guide_lint.py` and `scripts/harness/test_hook.py`; **advisory** (`continue-on-error`) | always |
| `complexity` | golangci-lint with only `gocyclo,gocognit,cyclop,funlen,nestif` (gate 15) plus an informational gocyclo report | always |
| `test` | `go build ./...`, `go vet ./...`, then `make coverage` (90% gate); uploads `coverage.out` | always |
| `bdd` | `go test ./... -run TestFeatures -v` | always; vacuous today |
| `integration` | `go test -tags=integration ./... -race -count=1` | always; vacuous today |
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
advisory architecture review on pull requests into `develop` that touch the domain,
application, `apis/` or `charts/`; it does nothing without the `ANTHROPIC_API_KEY`
secret.

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
- `HARNESS.md` lists `docs-api-drift`, `web`, `helm-lint`, `trivy-scan` and CodeQL as
  sensors; none is in `ci.yml`.

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
