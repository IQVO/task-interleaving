# ADR 0012: PICK is a first-class interleaving candidate

## Status

Accepted (2026-11-05). Extends [ADR 0002](./0002-stationtrip-and-policy-aggregates.md)
(the policy aggregate and its task-type catalogue) and [ADR 0003](./0003-local-copies-and-consumed-contracts.md)
(the consumed `TaskAvailable` contract); records the pick-batching wave 2c
findings and the dependency on fulfillment-execution's in-flight change.

## Context

The fleet is building pick batching (plan of 2026-11-05). Its wave 2c asks
this context one question: what does `task-interleaving` need so that a
`TaskAvailable` for a PICK task produces and ranks hint candidates exactly
like the announced types? The wave 1 spike (2026-11-05) established the ground
truth, re-verified here against this tree:

- `PICK` has been a valid `TaskType` since ADR 0002's catalogue
  (`policy.TaskPick`, `TaskType.Valid`). Nothing in the domain, the pool, the
  recommender or the hint validation special-cases it.
- Whether a site's hints may point at PICK is **per-site policy data**
  (`Settings.EligibleTaskTypes`), not code. No site policy is declared
  anywhere in the fleet today, and a policy-less site is inert by design
  (ADR 0002) — that rule is kept, not worked around.
- The only reason no hint names a PICK task today is upstream:
  fulfillment-execution gives a PICK task no placement, so it publishes no
  `TaskAvailable` for it (its ADR 0049/0050 scope is RELOCATE and REPLENISH).
  The consumed contract here already pins `task_type` as an enum that includes
  `PICK`, and the consumer accepts, stores and ranks an announced PICK
  unchanged — the fixtures of the existing e2e and consumer tests are PICK
  tasks already.
- The batching plan caps a batch at 4 tasks by default (decision D4);
  `MaxCandidates` is bounded 1..5 per site. Nothing proved that every layer
  between the pool and the issued hint keeps ALL candidates below the bound.
- Once PICK is announced it dominates task volume, so a site's pool grows from
  the handful of RELOCATE/REPLENISH tasks in today's fixtures to the release
  policy's WIP ceiling (1 000 open units) — the recommender had never been run
  over a pool of that shape.

What was genuinely missing, then, was not eligibility but proof: parity of
ranking across types, survival of the full candidate bound, and a performance
envelope at PICK volume.

## Decision

1. **No production code change.** PICK stays a first-class candidate through
   the existing per-site policy: a site that wants PICK hints declares
   `eligibleTaskTypes` including `PICK` and `maxCandidates >= 4` (the batching
   default) via the existing policy declaration (REST `PUT
   /sites/{siteId}/policy`, A2A, MCP). No default policy is invented; a site
   without one remains inert (ADR 0002).
2. **Parity is a tested invariant.** A domain test ranks a mixed
   PICK/RELOCATE/REPLENISH pool and asserts the order is detour-then-task-id
   only — types interleave, and a PICK ties with a REPLENISH by task id. An
   integration test (real Kafka consumer, real Postgres) feeds synthetic
   `TaskAvailable` fixtures — the producer's pinned contract as raw JSON — for
   a mixed pool and a completed PICK task, and asserts the issued hint's
   candidates interleave the three types by detour. The synthetic fixtures
   keep this path proven while the producer change is in flight.
3. **The candidate bound holds end to end.** `TaskPool.Available` has no
   `LIMIT`, the geometry batch reads every pool location, `Recommend` cuts at
   exactly the declared `MaxCandidates`, and `IssueHint` refuses more. A
   boundary test with seven qualifying tasks locks five kept at the ceiling
   (5) and four at the batching default (4); the integration test issues a
   five-candidate hint over the real adapters. No layer prunes below the
   declared bound. The bound (5) accommodates the batching default maximum
   (4).
4. **A tagged load suite records the envelope** (`make load`, build tag
   `load`, deliberately outside `make check`/`check-all` and CI). Measured
   with `-race` on the development machine of 2026-11-05:
   `Recommend` over a 1 000-task pool (the release WIP ceiling, ~98 % PICK):
   first call 2.4 ms, steady state 7.1 ms per call; over a 10 000-task pool
   (10x headroom): first call 22.8 ms, steady state 69 ms per call; the full
   `ApplyTaskCompleted` path (pool read + geometry batch + ranking + hint
   issue + persistence) over a 1 000-task pool: 24.7 ms end to end. Scaling is
   linear in pool size (10x pool ≈ 10x time); hints issue once per task
   completion, so the cost is noise at real completion cadence. The suite
   asserts generous walls (50 ms / 500 ms per call, 5 s end to end) so a
   complexity regression fails loudly instead of silently slowing completions.
5. **The announcement dependency is recorded, not duplicated here.**
   fulfillment-execution's pick-placement change (in flight as a sibling PR)
   is the only remaining gap: once it announces PICK tasks, they flow through
   this context with zero change — the consumed contract, the pool copy, the
   policy semantics and the published events are already PICK-ready, and no
   new field exists or is needed on either side.

## Consequences

- The sentences across this repo's documentation that say `TaskAvailable` is
  announced "only for RELOCATE and REPLENISH today" (the ecosystem
  integration page, the README, `apis/asyncapi.yaml`'s TaskAvailable
  description, the agent guides) remain true until the producer change lands,
  and then flip in the as-built documentation pass — this ADR is the record
  that the consumer side needs nothing.
- `apis/asyncapi.yaml` and `apis/openapi.yaml` are unchanged by this decision:
  no contract field changes, no new enum value (`PICK` was always in the
  catalogue), no new event.
- Sites must still declare a policy that includes PICK before any PICK hint
  is issued; the e2e/simulation declarations happen in the plan's wave 6.
- The load suite's walls are walls, not benchmarks: the logged numbers are
  machine-dependent and recorded here only as the first measurement of the
  PICK-volume envelope.
- If batching later raises the default maximum beyond 5 (plan decision D4 is
  a placeholder until measured), the `MaxCandidates` bound itself must be
  revisited in a new ADR — the domain rejects 6 today.
