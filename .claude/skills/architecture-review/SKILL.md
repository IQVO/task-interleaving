---
name: architecture-review
description: Bounded-context boundary and ADR-compliance review of a change (expensive, post-integration): hexagonal direction, cross-context coupling, contradicted ADRs. Invoke explicitly: /architecture-review [range].
disable-model-invocation: true
argument-hint: "[git range]"
---

Perform a bounded-context boundary and ADR-compliance review of the
current changes (or `$ARGUMENTS` if given, e.g. a branch/PR diff range).

This is EXPENSIVE relative to `/code-review` — it reasons about
cross-repo/cross-context implications, not just this diff's local
correctness. Use it post-integration (before merging a PR that touches
architecture, not on every small commit) or when asked explicitly to
check a design decision against this fleet's standing architecture.

## What to check, in priority order

1. **Hexagonal dependency direction.** Domain (`internal/domain/policy`,
   `travel`, `recommend`, `stationtrip`) depends on nothing internal but the
   domain; the application layer (built in wave 2) depends on domain + ports;
   adapters depend on application + domain and never on each other; only the
   `cmd/` wiring (wave 2) assembles every layer. Run
   `go test ./internal/architecture/... -v` first
   (`TestHexagonalArchitecture`) — if it's already red, report that and stop;
   don't hand-review what a fitness test already caught.
2. **Context map and exclusions, per `docs/adr/0001-task-interleaving-bounded-context.md`.**
   task-interleaving is a Supporting-subdomain **advisor**: it is a Conformist
   to facility-layout and fulfillment-execution (consumed Published Languages)
   and publishes `InterleavingHintIssued` back. A new outbound call to a
   sibling context, a live cross-context lookup, or anything that assigns work
   to a worker (instead of advising) contradicts ADR 0001. Flag it: it needs its
   own ADR, not a code change slipped in silently.
3. **Local-copy contract, per `docs/adr/0003-local-copies-and-consumed-contracts.md`.**
   Consumed types must match the producers' `apis/asyncapi.yaml` byte-for-byte
   and stay in the ADR 0003 and ADR 0004 tables; `TaskAvailable` and
   `TaskClaimed` are published by fulfillment-execution (`TaskAvailable` for RELOCATE tasks only); verify against its `apis/asyncapi.yaml` on `origin/develop` before relying on any other task type.
   Copies converge from events only, with the `*_MODE` switches defaulting to
   `permissive`.
4. **MCP additive-boundary rule (fleet-wide).** A change in an MCP inbound
   adapter must depend only on application/domain, and nothing else may depend
   on it. `TestMCPAdapterDependencyRule` in `internal/architecture` encodes it;
   confirm it's green. There is no MCP adapter in this repo yet.
5. **Pure recommender and replaceable travel model, per `docs/adr/0002-stationtrip-and-policy-aggregates.md`.**
   `recommend.Recommend` must stay free of I/O, clock and randomness; a new
   travel model sits behind `travel.Model` with its own ADR and is selected per
   policy, not hard-coded in the recommender.
6. **Kafka consumer-group pattern correctness.** The two consumers (fulfillment,
   layout) are stable process-and-commit consumers: group id from
   `FULFILLMENT_CONSUMER_GROUP` / `LAYOUT_CONSUMER_GROUP`, never a literal
   (`TestKafkaConsumerGroupNeverHardcodedInline`). A consumer that instead
   replays full history on every start needs a per-process-unique group id. Flag
   any consumer whose pattern doesn't match its actual replay behavior — this is
   a correctness bug (see `.claude/skills/how-to-add-an-integration-event/SKILL.md`).
7. **A new cross-repo contract with no companion documentation.** If the change
   introduces or alters a cross-repo contract (a new REST call, a new topic
   subscription, a new published type), check that an ADR documents the decision
   and whether a companion ADR is needed in the OTHER repo (see
   `.claude/skills/how-to-write-an-adr/SKILL.md`).
8. **Auth-reintroduction and sibling-call bans**, same as `/code-review`
   items 6-8, reasoned about more thoroughly: not just "is there a Bearer
   literal" but "does this change's INTENT require re-litigating the fleet-wide
   auth-removal decision", which needs its own ADR.

## Output format

State clearly: PASS (no architectural concerns), CONCERNS (list them,
each tied to the specific rule/ADR it would violate), or NEEDS-ADR (the
change is architecturally sound but undocumented — name what the ADR
should cover). Cite the specific file/rule/ADR for every finding; a
finding with no citation is not actionable.

This is advisory. It never blocks a merge on its own, and it never
modifies files. If a finding conflicts with a decision explicitly stated
in this repo's own accepted ADRs, defer to the ADR and say so — this
command reasons about the fleet's general conventions, not a higher
authority than the repo's own explicit guidance.
