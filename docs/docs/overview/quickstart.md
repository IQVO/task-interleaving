---
id: quickstart
title: Quickstart
sidebar_label: Quickstart
sidebar_position: 3
---

# Quickstart

This page covers only what runs on `develop` today: building, the test gates and
a throwaway program that drives the domain. There is **no server to start**: no
`cmd/`, no HTTP listener, no Kafka consumer, no database, no seed data and no
`curl` call that answers. The first REST call, the Postgres and Kafka setup and
the seeding steps will be added with the service; until then they would be
inventions.

## Prerequisites

| Tool | Version | Needed for |
| --- | --- | --- |
| Go | `1.27.1` (`go.mod`) | everything |
| `make` | any | the targets below |
| golangci-lint | `v2.13.1` (`GOLANGCI_VERSION` in the `Makefile`, pinned in CI) | `make lint`, `make check` |
| gremlins | `v0.6.0` (`GREMLINS_VERSION`) | `make mutation`, `make mutation-full` |
| govulncheck | latest | `make vuln` |
| Spectral (`@stoplight/spectral-cli`) | latest | linting the two specs, see [Testing](/docs/development/testing#ci-jobs) |
| Node 20 | | building this documentation site (`docs/`) |
| lefthook | optional | `lefthook install` wires `make fmt-check vet lint` at pre-commit and `make check` at pre-push |
| Docker | not needed today | `make integration` boots testcontainers, but the repo has no integration test |

The fleet runs a single Kafka broker (reachable at `localhost:9092` from the kind
cluster's external access) and the docker-compose Kafka is retired. Nothing in this
repo connects to it yet.

## Build and test

```bash
make help          # list the targets
make build         # go build ./...
make vet           # go vet ./...
make test          # go test ./... -race
make arch-test     # go test ./internal/architecture/... -v
make coverage      # tests with -race plus the 90% gate over the domain
make check-fast    # fmt-check, vet, arch-test, tests of the packages changed vs HEAD
make check         # fmt-check vet build lint test
make check-all     # check + coverage + arch-test + bdd
```

`make coverage` on `develop` ends with:

```text
Coverage: 100.0% (gate: 90%, scope: ./internal/domain/...,./internal/application/...)
```

The scope names `./internal/application/...`, which does not exist yet; `go test`
prints `warning: no packages being tested depend on matches for pattern
./internal/application/...` and the gate measures the domain only.

`make bdd` runs `go test ./... -run TestFeatures -v`. The repo has no godog
dependency and no `features/` directory, so every package reports
`[no tests to run]` and the target passes vacuously. `make integration` is the same
story: no file carries the `integration` build tag. See
[Testing](/docs/development/testing) for the full picture.

## Drive the domain by hand

The closest thing to a simulation on `develop` is calling the domain packages
directly. Create a throwaway program **inside the module** (the packages are under
`internal/`, so they cannot be imported from outside it). A directory starting
with `_` is ignored by `./...`, so it does not disturb `make build`; delete it when
you are done.

```go
// _scratch/main.go
package main

import (
	"fmt"
	"time"

	"github.com/claudioed/task-interleaving/internal/domain/policy"
	"github.com/claudioed/task-interleaving/internal/domain/recommend"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
	"github.com/claudioed/task-interleaving/internal/domain/travel"
)

func pt(x, y, z float64) *travel.Point { return &travel.Point{X: x, Y: y, Z: z} }

func main() {
	site, _ := policy.NewSiteID("SITE-1")
	pol, err := policy.New(site, policy.Settings{
		MaxDetourM:        25,
		MaxCandidates:     3,
		HintTTL:           90 * time.Second,
		EligibleTaskTypes: []policy.TaskType{policy.TaskPick, policy.TaskReplenish},
		VerticalWeight:    1.5,
	})
	if err != nil {
		panic(err)
	}

	origin := travel.Place{Location: "WH1-FWD-A01-01-01-A", Point: pt(4.2, 10.5, 0.4)}
	pool := []recommend.OpenTask{
		{ID: "task-9d10", Type: policy.TaskReplenish, Site: site, FromLocation: "WH1-FWD-A02-01-01-A", FromPoint: pt(10.0, 14.5, 0.4)},
		{ID: "task-9c2e", Type: policy.TaskPick, Site: site, FromLocation: "WH1-FWD-A01-02-01-A", FromPoint: pt(6.0, 12.7, 0.4)},
		{ID: "task-far", Type: policy.TaskPick, Site: site, FromLocation: "WH1-FWD-A19-01-01-A", FromPoint: pt(90, 10.5, 0.4)},
		{ID: "task-nogeo", Type: policy.TaskPick, Site: site, FromLocation: "WH1-FWD-A05-01-01-A"},
		{ID: "task-pack", Type: policy.TaskPack, Site: site, FromLocation: "PACK-01", FromPoint: pt(5, 11, 0)},
	}
	cands, err := recommend.Recommend(recommend.Input{
		Site: site, Origin: origin, Pool: pool, Policy: pol,
		Completed: "task-8a1f", Model: recommend.RectilinearFor(pol),
	})
	if err != nil {
		panic(err)
	}
	for _, c := range cands {
		fmt.Printf("rank %d %s %s at %s detour %.1f m\n", c.Rank, c.TaskID, c.TaskType, c.Location, c.DetourM)
	}

	trip, _ := stationtrip.New("STN-07")
	t0 := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	trip.RecordCompletion("task-8a1f", t0, stationtrip.Position{Site: site, Place: origin})
	if err := trip.IssueHint("hint-demo-1", cands, pol, t0); err != nil {
		panic(err)
	}
	fmt.Println("claim of a non-candidate consumes:", trip.RecordClaim("task-far", t0.Add(10*time.Second)))
	fmt.Println("claim of rank 1 consumes:", trip.RecordClaim("task-9c2e", t0.Add(20*time.Second)))
	for _, e := range trip.DomainEvents() {
		fmt.Printf("event %s (station version %d)\n", e.EventName(), e.StationVersion())
	}
}
```

```bash
go run ./_scratch
```

Output from a real run of the same program (with a few extra `Println` calls):

```text
policy version: 1 eligible: [PICK REPLENISH]
rank 1 task-9c2e PICK at WH1-FWD-A01-02-01-A detour 4.0 m
rank 2 task-9d10 REPLENISH at WH1-FWD-A02-01-01-A detour 9.8 m
completion recorded: true <nil>
hint hint-demo-1 Active expires 2026-10-17T12:01:30Z
claim of a non-candidate consumes: false
claim of rank 1 consumes: true
hint state now: Consumed trip version: 3
event HintIssued (station version 2)
event HintConsumed (station version 3)
origin without geometry: recommend: the origin has no known geometry
```

What to notice, each one a rule in the code:

- `task-far` is 85.8 m away (over `MaxDetourM` 25), `task-nogeo` has no point (a
  task with unknown geometry is skipped, never treated as distance 0) and
  `task-pack` is a `PACK` task while the policy allows only `PICK` and `REPLENISH`.
- The detour of `task-9c2e` is `|4.2-6.0| + |10.5-12.7| + 1.5*|0.4-0.4| = 4.0`.
- Claiming a task that is not a candidate leaves the hint Active; claiming a
  candidate while the hint is unexpired marks it Consumed and raises `HintConsumed`.
- An origin with no geometry is `ErrOriginUnknown`: nothing to measure from.

## Building these docs

```bash
cd docs
npm ci
npm run build       # also fails on broken links and anchors
npm run gen-api-docs  # regenerate docs/api-reference/rest from apis/openapi.yaml
```

The REST reference pages are generated from `apis/openapi.yaml`; run the generator
after any spec change and commit the result, because `sidebars.ts` imports the
generated `sidebar.ts`.

## What comes later

Starting a local Postgres, pointing the service at the fleet Kafka, declaring a
policy with `PUT /sites/{siteId}/policy` and reading the first hint are the
natural next steps of this page. They are not documented because none of that
code is on `develop`.
