---
name: code-review
description: Fast pre-commit semantic review of the uncommitted changes or a given git range: domain logic in the wrong layer, missing failure-path tests, adapter concerns in domain types, impure ports. Advisory counterpart to make check. Invoke explicitly: /code-review [range].
disable-model-invocation: true
argument-hint: "[git range]"
---

Perform a pre-commit semantic code review of the current uncommitted
changes (or, if `$ARGUMENTS` names a branch/commit range, review that
diff instead — e.g. `/code-review origin/develop..HEAD`).

This is a fast, local, advisory pass — the counterpart to `make check`'s
mechanical checks (fmt/vet/lint/test), not a replacement for them. Run
`make check` FIRST; don't spend this review's attention on anything a
linter already catches.

## What to look for, in priority order

1. **Domain logic leaking into the wrong layer.** Business rules belong
   in `internal/domain/` (`policy`, `travel`, `recommend`, `stationtrip`), not
   in an HTTP handler, a Kafka consumer, or a repository adapter. A handler in
   the HTTP adapter (built in wave 2) must do nothing beyond decode -> call use
   case -> encode; the ranking rule lives in `recommend.Recommend` and the hint
   invariants in `stationtrip.StationTrip`, never in a consumer.
2. **New decision logic with no failing-path test.** Every new or changed rule
   in `internal/domain/` needs a test for its refusal path, not just the happy
   path (see `internal/domain/stationtrip/trip_test.go` and
   `internal/domain/policy/policy_test.go`). The same applies to a use case in
   the application layer once it exists. This is the most common gap
   `make coverage`'s 90% gate still lets through.
3. **A domain type carrying an adapter concern.** JSON tags, SQL column names,
   CloudEvents attribute names or HTTP status codes on a type under
   `internal/domain/` is a boundary violation the fitness tests won't catch
   (they check import direction, not tag presence) — flag it by eye. Domain
   events (`stationtrip.HintIssued` etc.) are facts, not wire messages.
4. **A driven port that isn't a pure interface**, or a use case constructing a
   concrete adapter directly instead of depending on a port. Ports contain
   interfaces only; the one port that exists today is `travel.Model`.
5. **Error handling that swallows or over-wraps.** Domain sentinel errors
   (`policy.ErrInvalidCandidates`, `stationtrip.ErrNoCandidates`,
   `recommend.ErrOriginUnknown`, ...) should map cleanly to the RFC 7807 problem
   types in `apis/openapi.yaml` (see `.claude/rules/rest-api.md`) rather than
   being stringified and re-wrapped on the way out.
6. **A new Kafka `GroupID` assigned an inline string literal** rather than a
   value read from the environment — this fleet has a real incident
   (wes-work-planning#67) from exactly this pattern;
   `TestKafkaConsumerGroupNeverHardcodedInline` catches it in CI, but flag it
   here too.
7. **A newly reintroduced auth/bearer/JWT check.** Every REST/MCP endpoint in
   this fleet is deliberately unauthenticated (2026-09-11 fleet-wide revert) — a
   reintroduced auth check is very likely accidental and should be flagged even
   if the code itself looks correct (`TestNoAuthMiddlewareReintroduced`).
8. **Anything that would surprise the sibling-context boundary.** Per
   `docs/adr/0001-task-interleaving-bounded-context.md` this service makes no
   outbound calls to sibling contexts and never assigns work; check the diff
   doesn't reintroduce exactly that. Events leave only through the transactional
   outbox, not a direct Kafka write.

## Output format

For each finding: file:line, a one-sentence description of the issue, and
a one-sentence suggested fix. Group findings by severity
(blocking/should-fix/nit). If nothing needs fixing, say so plainly — don't
manufacture findings to justify the review.

This command never modifies files or runs `git commit`/`git push`. It
only reports.
