# task-interleaving

Task Interleaving: WES-tier Supporting bounded context of the warehouse-systems fleet. When a station completes a task it ranks the open tasks of the site by travel distance and issues an advisory hint, so operators chain compatible work instead of travelling empty. fulfillment-execution stays pull-based and uses the hint only as a tie-break inside one priority band. Hexagonal Go, Postgres, REST, Kafka (CloudEvents 1.0).

- Design and pinned contracts: `docs/adr/` and the fleet plan `.hermes/plans/2026-10-17_005500-pick-face-replenishment-and-task-interleaving.md`.
- Scaffolded from `warehouse-harness-template` (v3). Guides: `.claude/rules/`, sensors: `HARNESS.md`.
- Commands: `make check-fast`, `make check-all`, `make integration`.
