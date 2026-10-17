---
name: domain-review
description: Ubiquitous-language drift review of a change against .claude/rules/domain-model.md: renamed or invented terms, aggregate-invariant leaks. Invoke explicitly: /domain-review [range].
disable-model-invocation: true
argument-hint: "[git range]"
---

Perform a ubiquitous-language drift review of the current changes (or
`$ARGUMENTS` if given), comparing new/changed code against
`.claude/rules/domain-model.md` and `docs/adr/0002-stationtrip-and-policy-aggregates.md`.

Ubiquitous language drift is the quiet failure mode DDD is supposed to
prevent: code that technically works but silently renames, reshapes, or
duplicates a concept the domain-model doc already named — so future
readers can no longer map code to domain conversation.

## What to check, in priority order

1. **A new type/field/method that duplicates an existing domain concept
   under a different name.** The domain-model doc already names the concepts:
   **Hint**, **Candidate**, **Detour** (`DetourM`), **StationTrip**,
   **InterleavingPolicy**, **Open-task pool**. A new type such as `Suggestion`,
   `NextTask`, `Distance` for the detour, or `Assignment` for a hint is drift —
   even if the math is correct, it fragments the vocabulary. Worse,
   `Assignment` implies the service assigns work, which ADR 0001 forbids. Flag it
   and point at the existing name.
2. **A domain type/method named in implementation terms instead of
   domain terms.** `internal/domain/` code should read like the ubiquitous
   language, not like database/HTTP vocabulary — a method called `UpdateRow`
   or `PatchState` where the domain would say `IssueHint`, `RecordCompletion`,
   `RecordClaim` or `Expire` is drift, and the fitness tests won't catch it
   because it's a naming problem, not an import-direction problem.
3. **An invariant enforced in code that the domain-model doc doesn't
   mention, or vice versa.** If a new rule was added (a new validation, a new
   state-transition guard on `stationtrip.HintState`), the domain-model doc and
   ADR 0002 should be updated in the SAME PR — an undocumented invariant is
   invisible to the next person who touches that aggregate and may be removed
   as dead code.
4. **A value object that should be closed but was implemented open (or vice
   versa).** `policy.TaskType` is a deliberately CLOSED set (`PICK`, `PACK`,
   `REBIN`, `SLAM`, `RELOCATE`, `REPLENISH`; unknown values are refused) and
   `stationtrip.HintState` is a closed state machine with three terminal states.
   Check any new categorical field against whether the domain wants an open or
   closed set, and flag a mismatch either way.
5. **A cross-aggregate rule implemented as a cross-aggregate call instead of
   an explicit local check, or vice versa.** "Currently in the open-task pool"
   is a property of the recommender's input, not of `StationTrip`, which only
   re-checks the shape and bounds of what it is handed (ADR 0002). A change that
   makes the aggregate or recommender reach out to another context, a clock or a
   database would be a real regression even if it still "works".
6. **New terminology introduced without updating the domain-model doc.** If
   new code introduces a genuinely new domain concept the doc doesn't yet name,
   that's not necessarily wrong — but the doc needs a new entry in the SAME PR,
   or the vocabulary silently forks between prose and code.

## Output format

For each finding: the term/concept involved, where it appears in the
domain-model doc (or "not yet documented"), where the drift appears in
code, and a one-sentence recommendation (rename, document, or confirm
it's an intentional new concept). If the changes introduce no new domain
concepts and use existing vocabulary correctly, say so plainly.

This command never modifies files. It is advisory input for the author
to act on, not a blocking gate.
