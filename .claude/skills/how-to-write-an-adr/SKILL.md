---
name: how-to-write-an-adr
description: Write an Architecture Decision Record in this repo's numbering and format, including companion ADRs for cross-repo changes. Use when a design decision should be recorded or a change contradicts an existing ADR.
---

# How to write an ADR

Use when a change is architecturally significant — a new bounded-context
integration, a reversal of a prior decision, a cross-repo contract change,
or anything a future reader would otherwise have to reverse-engineer from
the diff. Not every change needs one: a bug fix or a routine feature
addition inside an already-decided architecture doesn't.

## Numbering and location

`docs/adr/NNNN-kebab-case-title.md`, four-digit zero-padded, sequential.
Check the highest existing number (`git ls-tree --name-only origin/develop
docs/adr/` and pick the next integer, never reuse or guess). Today the set is
0001 bounded context, 0002 aggregates and policy ports, 0003 local copies and
consumed contracts, 0004 CloudEvents envelope and type catalogue.

Add the new ADR to the table in `docs/adr/index.md` (number, link, title, the
status copied from the ADR's own `## Status`). Unlike a Docusaurus ADR, the
ADR files of this repo carry no frontmatter; only `docs/adr/index.md` does.
Link ADRs in the index as `/docs/adr/<file-name-without-.md>`.

## Format

```markdown
# ADR NNNN: title (a short noun phrase)

## Status

Accepted (YYYY-MM-DD).   <!-- or Proposed | Deprecated | Superseded by ADR NNNN -->

## Context

The forces at play — technical, business, constraints — that make this
decision necessary, in the past tense, for someone who wasn't there. State
the alternatives seriously considered, not just the one chosen.

## Decision

What was decided, as an active present-tense declaration. Be specific about
the mechanism, not just the intent: a reader should be able to implement the
same decision without follow-up questions. Tables of fields and rules are
welcome (see ADR 0002).

## Consequences

What becomes easier, what becomes harder, what future work this creates or
forecloses. Be honest about the downsides.
```

The `## Decision` section is where the editing effort goes. Model examples in
this repo: `docs/adr/0002-stationtrip-and-policy-aggregates.md` (exact
invariants, state diagram, the pure recommender and its ports) and
`docs/adr/0003-local-copies-and-consumed-contracts.md` (the exact consumed
types, how each copy converges, the mode and consumer-group switches, and a
"contradiction with the plan, fixed here" note when a producer's contract
disagrees).

Rules specific to this repo's ADRs:

- Where an ADR records a cross-repo contract, verify it against the producer's
  `apis/asyncapi.yaml` on its `origin/develop`, and date the verification.
  Mark a contract that does not exist yet as pending (as ADR 0003 does for
  producer change P2).
- Adding a CloudEvents type to `apis/asyncapi.yaml` also needs its line in the
  type catalogue of `docs/adr/0004-cloudevents-envelope-and-type-catalogue.md`
  (`TestEventCatalogueMatchesContract`).
- In MDX wrap any `{sku}`-style path in backticks (`/sites/{siteId}/policy`),
  or the docs build reads it as an expression.

## Superseding an earlier ADR

Don't edit the old ADR's Decision section. Change its `## Status` to
`Superseded by ADR NNNN` (a one-line patch, and the status column of the index),
and open the new ADR referencing it: `Accepted (date). Supersedes ADR NNNN.`

## Cross-repo decisions: use a companion ADR, not one repo's private opinion

When a decision genuinely spans two bounded-context repos (for example the
pending producer change P2 in fulfillment-execution and the consumer here, or
the consumer change P3 that lets fulfillment-execution use
`InterleavingHintIssued`), write ONE ADR per repo, each referencing the other
explicitly as "the companion ADR" with a one-line description of the split of
responsibility. Don't write the decision once and expect the other repo's
readers to find it; each bounded context's docs are read independently. Before
citing a sibling's ADR number, list that sibling's `docs/adr/` folder on its
`origin/develop`.

## After writing

Check that every relative link and every `docs/adr/` reference resolves, that
`docs/adr/index.md` lists the new ADR, and run
`python3 scripts/harness/guide_lint.py` if a guide cites it. There is no
docs-site build in this repo; if one is added, run it locally before opening
the PR, since a broken ADR link or malformed frontmatter fails it.
