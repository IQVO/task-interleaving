# CLAUDE.md — task-interleaving

`task-interleaving` is a Supporting bounded context (`wes` tier) of the `warehouse-systems` fleet (GitHub org `IQVO`, Go module `github.com/claudioed/task-interleaving`). It chains compatible tasks to cut deadheading: when a station completes a task it ranks the open tasks of the same site by travel distance from where the operator now stands and issues an advisory hint. It owns `InterleavingPolicy` and `StationTrip` and nothing else: the task lifecycle and `claimNext` stay in fulfillment-execution, the map and geometry in facility-layout. Study project: not production software.

Design source of truth: `docs/adr/` (ADR 0001 context, 0002 aggregates, 0003 local copies and consumed contracts, 0004 CloudEvents catalogue, 0005 persistence and evaluation semantics).

## Hard rules (each one is fitness-tested or cost an incident)

1. **Hexagonal dependency rule.** `internal/domain` depends on nothing internal but domain; `internal/application` only on domain and application; adapters never depend on each other; only `cmd/` wires layers. `make arch-test` enforces it.
2. **CloudEvents 1.0 is MANDATORY** on every Kafka message produced or consumed. Build/decode ONLY through `internal/adapters/kafka/cloudevents/`; `type` is `com.warehouse.wes.task-interleaving.<entity>.<Event>` (`stationtrip.InterleavingHintIssued` on the integration topic; `InterleavingHintConsumed|Expired` and `policy.InterleavingPolicyDeclared` analytics-only). Payloads: `apis/asyncapi.yaml`; catalogue: ADR 0004.
3. **Events leave through the transactional outbox only**, in the same unit of work as the aggregate save. The relay is built with `WithMaxAttempts` (`OUTBOX_MAX_ATTEMPTS`, default 20, 0 = unlimited): `TestOutboxRelaysPassAttemptCap` fails otherwise. At most ONE Active hint per station (partial unique index): only a NEW hint supersedes the previous one; a newer completion with no candidates leaves the old hint to expire (ADR 0005).
4. **Advisory only.** A hint never assigns and never claims: there is no `assign` anywhere. Candidates are issued from all priority bands; fulfillment-execution, which owns band ordering, is to use a hint purely as a tie-break inside one band and ignore it when absent or expired (that consumer side is planned, not built). With no declared `InterleavingPolicy` for a site NO hint is issued (inert, no invented defaults).
5. **The recommender is pure and deterministic** (travel model `rectilinear-v1` behind a `TravelModel` port, from facility-layout geometry): same input, same output; rank by detour, then task id. The open-task pool, task-to-station claims and location geometry are event-fed read models, not aggregate state. `POST /recommendations` is a read-only simulation: it writes nothing and emits nothing.
6. **No live lookups of sibling contexts** (REST or MCP).
7. **Runtime.** The service runs only on real Postgres and real Kafka (ADR 0006): `DATABASE_URL`, `KAFKA_BROKERS`, `FULFILLMENT_CONSUMER_GROUP` and `LAYOUT_CONSUMER_GROUP` are mandatory and a missing one is a boot error that names it, before the listener binds. Boot migrations, the outbox relay, both consumers and the hint-expiry worker (`HINT_EXPIRY_INTERVAL`, default 5s) are always on; `FULFILLMENT_MODE` and `LAYOUT_MODE` only accept `kafka` (`permissive` is rejected). `/readyz` checks the drain gate, Postgres and that a Kafka broker answers a metadata request; it never checks topics (the broker auto-creates them on first write; ADR 0006 Amendment 1). `cmd/**` never imports `internal/adapters/outbound/memory` (`TestRuntimeBinaryHasNoInMemoryAdapters`); the memory adapters are test doubles. `TaskAvailable` and `TaskClaimed` are published by fulfillment-execution (its ADR 0049), `TaskAvailable` only for tasks that carry bins (RELOCATE and REPLENISH today, ADRs 0049 and 0050), so the task pool holds those task types only. Nothing in fulfillment-execution consumes `InterleavingHintIssued` yet. Env table: `.claude/rules/integration-events.md`.
8. **`version` guards every write** (a stale or missing `If-Match` on the policy PUT is 412, a lost race 409); POSTs require an `Idempotency-Key`; 5xx outcomes are never stored.
9. **No auth layer on REST or MCP** (fleet-wide revert 2026-09-11): `TestNoAuthMiddlewareReintroduced`. MCP is additive and not built yet (a separate binary with read tools only, when it comes).
10. **Planned, not built:** fulfillment-execution consuming hints, aisle-aware routing (aisle geometry, cross-aisles, fixed structures), equipment-aware chaining, multi-task chains beyond the next task, MCP.
11. **An ADR comes first** for a new travel model version, a cross-context contract or any auth. ADRs live in `docs/adr/`.
12. **Packaging and layout.** The `Dockerfile` and `charts/task-interleaving` ship the real runtime (ADR 0006: Postgres and Kafka, required values, no `*_MODE`). No Go file lives at the repo root: godog lives in `features/` (package `features_test`, Paths "."), binaries in `cmd/`, the rest in `internal/`. `TestNoGoFilesAtRepoRoot` enforces it.

## Commands

```bash
make check-fast   # fmt-check + vet + arch-test + tests of changed packages: run before saying "done"
make check        # fmt-check vet build lint test
make check-all    # check + coverage (90% gate on domain+application) + arch-test + bdd
make integration  # real Postgres/Kafka via testcontainers (needs Docker)
make vuln         # govulncheck: run it after any go get / go mod tidy
make mutation     # gremlins on the domain packages (CI job mutation-fast, blocking)
make guide-lint   # these guides: skills load, references resolve, context budget
```

## Where things are written down (read before editing the matching area)

- `.claude/rules/domain-model.md`, `rest-api.md`, `integration-events.md`; `.claude/rules/fleet/*.md` are copied from the harness template, never edit them here.
- `docs/adr/` decisions; `apis/openapi.yaml`, `apis/asyncapi.yaml` contracts; `HARNESS.md` sensors.

Scaffolded from `warehouse-harness-template`.
harness-template: v3

<!-- harness:scoped-rules:start (generated by tools/migrate_v3.py in warehouse-harness-template; do not hand-edit) -->
## Scoped rules and harness

Claude Code loads each rule below automatically when you touch the matching paths. OpenCode and Codex do NOT: read the rule BEFORE editing matching files.

| When touching | Read |
|---|---|
| `internal/domain/**`, `internal/application/**`, `features/**` ... | `.claude/rules/domain-model.md` |
| `internal/adapters/**/kafka/**`, `internal/adapters/outbound/events/**`, `apis/asyncapi*` | `.claude/rules/integration-events.md` |
| `internal/adapters/inbound/http/**`, `apis/openapi*.yaml`, `apis/openapi/**` | `.claude/rules/rest-api.md` |

Hooks (`scripts/harness/hook.py`, wired for Claude Code, Codex and OpenCode) block pushes to develop/main, `--no-verify`, bare `rm -rf`, and edits to generated files, and feed gofmt/vet findings back after each edit. Before saying "done" run `make check-fast`; the full gate is `make check-all`. `HARNESS_OFF=1` disables the hooks when debugging the harness itself.
<!-- harness:scoped-rules:end -->
