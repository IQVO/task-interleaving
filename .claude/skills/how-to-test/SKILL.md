---
name: how-to-test
description: Write or review tests and diagnose a failing coverage, mutation, bdd or integration CI job: the four test layers, the 90% gate, gremlins threshold semantics, the testcontainers rule. Use when adding tests, killing a surviving mutant, or fixing a red check.
---

# How to test

Use when writing or reviewing tests in this repo, or diagnosing a failing
`coverage`/`mutation-fast`/`bdd`/`integration` CI job. This fleet's quality
bar is layered — passing `go test` is necessary but is the WEAKEST signal
of the four; mutation testing exists specifically because green tests can
assert nothing.

## The four layers, in order of what they actually prove

1. **Unit tests** (`go test ./...`) — prove the code runs and returns
   SOMETHING. Table-driven, no real network/DB call. Domain tests live next
   to the code: `internal/domain/stationtrip/trip_test.go`,
   `internal/domain/recommend/recommend_test.go`,
   `internal/domain/policy/policy_test.go`,
   `internal/domain/travel/travel_test.go`. Use cases (wave 2) are tested
   against an in-memory adapter.
2. **Coverage** (`make coverage`, 90% gate on
   `./internal/domain/...,./internal/application/...`) — proves lines
   executed. Proves nothing about whether the test asserted the right
   thing.
3. **Mutation testing** (`make mutation`, gremlins; there is no
   `mutation-fast` make target — that is the name of the CI job, which runs
   the same loop) — proves the tests actually ASSERT, not merely execute. A
   mutant is a deliberately broken version of the code (`<` -> `<=`, `+` ->
   `-`); if the suite still passes against it, it "survived" (LIVED) and no test
   would catch that bug in production. `make mutation` mutates each domain
   package on its own: `stationtrip`, `recommend`, `travel`, `policy`
   (`MUTATION_FAST_PKGS` in the `Makefile`); `make mutation-full` is the
   exhaustive scheduled run.
4. **BDD / behaviour** (`make bdd`, godog) — proves the use cases behave as
   specified: `features/*.feature` plus the runner and steps in the same
   directory (`features/features_test.go`, package `features_test`) drive the real
   use cases over the in-memory adapters. No Go file may live at the repository
   root (`TestNoGoFilesAtRepoRoot`).

Also: `make arch-test` (the fitness tests in `internal/architecture`),
`make integration` (needs Docker), `make vuln`.

## Mutation testing: `<=` fails, not `>=`

`.gremlins.yaml` sets `efficacy` and `mutant-coverage` thresholds that
gremlins fails on if the MEASURED score is `<=` the threshold, so a threshold
must sit strictly BELOW what the repo can meet. Read the comment in
`.gremlins.yaml` for the current baseline: all four domain packages measured
100% efficacy and 100% mutator coverage with 0 lived mutants, and the
thresholds are 90. When you deliberately lower coverage of a package (rare,
for example removing dead code), lower the threshold in the SAME PR with a
dated comment explaining why — never silently.

A new package under `internal/domain/` must be added to `MUTATION_FAST_PKGS` in
the `Makefile` AND to the `mutation-fast` loop in `.github/workflows/ci.yml`.

## Three real pitfalls that have each cost a real CI failure in this fleet

### 1. Zero/origin-value fixtures hide arithmetic mutants

A test built around zero-valued operands (a point at the origin `(0,0,0)`) makes
`a - b` and `a + b` identical, so a `-` to `+` mutant survives even though
coverage looks complete. `travel.Rectilinear.Distance`
(`|dx| + |dy| + VerticalWeight*|dz|`) needs fixtures where EVERY operand and
every per-axis delta is distinct and non-zero, a `VerticalWeight` other than 0
and 1, and the test must assert the exact expected value (and `travel.Round`'s
0.1 m rounding), not just "no error".

### 2. Boundary guards need the boundary value itself

A test for `if x < 0 { return err }` that only tries `-1` and `42` never
exercises `0`, so a `CONDITIONALS_BOUNDARY` mutant (`<` to `<=`) survives. This
domain is full of boundaries: `MaxCandidates` 1 and 5 (and 0 and 6), a detour
EXACTLY equal to `MaxDetourM` (it qualifies), `Expire(now)` exactly at
`expires_at` (expires) and `RecordClaim` exactly at `expires_at` (too late),
`MaxDetourM` just above 0. Every guard needs an explicit test of the boundary
value itself.

### 3. Tie-break / near-equivalent mutants: know when NOT to chase them

A `<` to `<=` mutant on a comparison that only diverges on an exact tie is
undetectable by a test whose values are all distinct. Here the tie-break IS a
specified rule (rank by `DetourM`, then `TaskID`, ADR 0002), so
`recommend.Recommend` MUST have a tie test — that one is a real invariant. Do
NOT force an artificial tie into a place where the order is unspecified: it pins
an arbitrary order as if it were an invariant. Record a genuinely
near-equivalent survivor in the PR description (and in a mutation triage doc if
the repo has one) and move on. The same applies to a guard whose boundary is
structurally unreachable: test the reachable edge, don't chase the other side.

## Diagnosing a `mutation-fast` CI failure: diff against develop, don't chase every LIVED line

```bash
gremlins unleash ./internal/domain/stationtrip   # on your branch, per package
```

Compare with the same run on `origin/develop` (in a separate worktree, never by
stashing). Only entries NEW on your branch are your regression. Confirming the
survivor SET is unchanged from `origin/develop`, not just that the percentage
cleared the `.gremlins.yaml` gate, is the real proof a fix didn't get lucky on
the threshold. Today the baseline is zero survivors, so any LIVED line is yours.

## Kafka/Postgres integration tests: testcontainers, never a skip-gate

A `-tags=integration` test touching Kafka or Postgres MUST start its own
container via `testcontainers-go` (`modules/postgres` / `modules/kafka`).
Never gate on `os.Getenv("KAFKA_BROKERS")` or `os.Getenv("DATABASE_URL")` +
`t.Skip(...)`, and never hardcode `localhost:9092`. This fleet's CI
`integration` job provisions NO service containers (`make integration` needs
only Docker) — a skip-gated test silently skips in CI and proves nothing there.
A fitness test per technology enforces it
(`TestKafkaIntegrationTestsUseTestcontainers`,
`TestPostgresIntegrationTestsUseTestcontainers`). Share one container per
package through a helper, use a unique topic per test, and create topics
explicitly then poll for the partition leader before the first read or write.
Integration tests for the consumers and the outbox are wave 2; the first Kafka
test that exists is the writer test in
`internal/adapters/outbound/kafka/writer_test.go`.

## Verify before opening the PR

```bash
make check-all   # check + coverage + arch-test + bdd (the full local gate)
```

`check-all` does not include `mutation` or `vuln`. Run `make mutation` (and
`make vuln`) explicitly — CI runs them, so a PR can pass your local check and
still go red otherwise.
