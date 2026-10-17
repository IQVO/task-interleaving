---
id: quickstart
title: Quickstart
sidebar_label: Quickstart
sidebar_position: 3
---

# Quickstart

Build the service, run it in-memory, make the first calls, then run it against
Postgres and wire Kafka. Every response below was captured from a real run of
`cmd/api` built from this commit (driven with Python `urllib`; `curl` returns the
same bodies).

## Prerequisites

| Tool | Version | Needed for |
| --- | --- | --- |
| Go | `1.27.2` (`go.mod`) | everything |
| `make` | any | the targets below |
| Docker | any recent | the Postgres container below and `make integration` (testcontainers) |
| golangci-lint | `v2.14.0` (`GOLANGCI_VERSION` in the `Makefile`, pinned in CI) | `make lint`, `make check` |
| gremlins | `v0.6.0` (`GREMLINS_VERSION`) | `make mutation`, `make mutation-full` |
| govulncheck | latest | `make vuln` |
| Spectral (`@stoplight/spectral-cli`) | latest | linting the two specs, see [Testing](/docs/development/testing#ci-jobs) |
| Node 20 | | building this documentation site (`docs/`) |
| lefthook | optional | `lefthook install` wires `make fmt-check vet lint` at pre-commit and `make check` at pre-push |

The fleet runs **one** Kafka broker (in-cluster, reachable at `localhost:9092`
through the kind cluster's external access); the docker-compose Kafka is retired.
You only need it for the consumers and the outbox relay, both optional.

## Build, test, run

```bash
make help          # list the targets
make build         # go build ./...
make test          # go test ./... -race
make arch-test     # go test ./internal/architecture/... -v
make bdd           # go test ./... -run TestFeatures -v   (68 scenarios)
make coverage      # tests with -race plus the 90% gate over domain + application
make check         # fmt-check vet build lint test
make check-all     # check + coverage + arch-test + bdd
make integration   # go test -tags=integration ./... -race -count=1 (needs Docker)
```

`make coverage` ends with `Coverage: 99.9% (gate: 90%, scope:
./internal/domain/...,./internal/application/...)` on this commit.

Build and start the binary (in-memory mode, no database, no Kafka):

```bash
go build -o task-interleaving ./cmd/api
HTTP_ADDR=127.0.0.1:18080 ./task-interleaving
```

The startup log (JSON lines on stdout):

```text
{"level":"INFO","msg":"DATABASE_URL not configured; using in-memory adapters (no outbox relay, Idempotency-Key not enforced)"}
{"level":"INFO","msg":"no consumer is in kafka mode; the local copies are whatever the database holds"}
{"level":"INFO","msg":"outbox relay off: in-memory mode has no outbox"}
{"level":"INFO","msg":"http server listening","addr":"127.0.0.1:18080"}
{"level":"INFO","msg":"hint expiry worker running","interval":"5s"}
```

(`time` fields omitted.) Stop it with Ctrl-C; it flips `/readyz` to 503, waits
`SHUTDOWN_DRAIN_DELAY` (5 s) and drains.

## First calls (in-memory)

REST is unauthenticated. Probes, then declare a policy:

```bash
curl -s localhost:18080/healthz    # {"status":"ok"}
curl -s localhost:18080/readyz     # {"status":"ready"}

curl -si -X PUT localhost:18080/sites/SITE-1/policy \
  -H 'Content-Type: application/json' \
  -d '{"maxDetourM":25,"maxCandidates":3,"hintTtlSeconds":90,"eligibleTaskTypes":["PICK","REPLENISH"],"verticalWeight":1.5}'
```

```text
200 OK, header ETag: "1", Content-Type: application/json
{"siteId":"SITE-1","maxDetourM":25,"maxCandidates":3,"hintTtlSeconds":90,"eligibleTaskTypes":["PICK","REPLENISH"],"verticalWeight":1.5,"version":1,"declaredAt":"2026-10-17T13:37:50.88072Z"}
```

Replacing a policy needs the current version as `If-Match`; the first
declaration must not send one. Observed answers:

| Request | Answer |
| --- | --- |
| `GET /sites/SITE-1/policy` before any `PUT` | `404` `policy-not-found`: the site is inert |
| second `PUT` without `If-Match` | `412` `version-mismatch` ("if-match does not match the current version") |
| `PUT` with `If-Match: "1"` and `maxCandidates: 4` | `200`, `ETag: "2"`, `"version":2` |
| `PUT` with `maxCandidates: 9` | `400` `invalid-policy`, detail `maxCandidates must be between 1 and 5` |
| `GET /hints` | `200 {"items":[]}` |
| `GET /hints?limit=0` | `400` `invalid-limit` |
| `GET /stations/STN-1` | `404` `station-not-found` ("no completion has been recorded for the station") |
| `POST /recommendations` `{"stationId":"STN-1"}` | `422` `station-position-unknown` ("send fromLocation") |
| `POST /recommendations` `{"stationId":"STN-1","fromLocation":"NOWHERE"}` | `422` `origin-unknown` ("no geometry is known for location NOWHERE") |

All errors are `application/problem+json`, for example:

```json
{"type":"https://errors.task-interleaving.warehouse-systems.dev/policy-not-found","title":"Interleaving policy not found","status":404,"detail":"the site has not declared an interleaving policy","instance":"/sites/SITE-1/policy"}
```

In-memory mode has no task pool or layout, because nothing feeds them: the
consumers write into the same process memory, and with no Kafka there is no
input. To see a real recommendation run against Postgres.

## Run against Postgres

Start a throwaway database and the service (migrations run at boot):

```bash
docker run -d --rm --name ti-pg -e POSTGRES_PASSWORD=pw -e POSTGRES_DB=ti \
  -p 127.0.0.1:55432:5432 postgres:16-alpine

DATABASE_URL='postgres://postgres:pw@127.0.0.1:55432/ti?sslmode=disable' \
HTTP_ADDR=127.0.0.1:18080 ./task-interleaving
```

Boot log: `postgres adapters configured`, then
`outbox relay off: KAFKA_BROKERS is not set; events accumulate in outbox_events
until it is` (WARN). `schema_migrations` reads `version 1, dirty f` and the public
schema holds `candidates, hints, idempotency_keys, interleaving_policies, locations,
outbox_events, processed_events, schema_migrations, station_trips, task_pool, zones`.

Declare a policy, then look at the outbox:

```bash
curl -s -X PUT localhost:18080/sites/SITE-1/policy -H 'Content-Type: application/json' \
  -d '{"maxDetourM":25,"maxCandidates":3,"hintTtlSeconds":90,"eligibleTaskTypes":["PICK"]}'
docker exec ti-pg psql -U postgres -d ti -At \
  -c "select id,topic,event_type,subject,attempts from outbox_events"
# 1|warehouse.task-interleaving.analytics|com.warehouse.wes.task-interleaving.policy.InterleavingPolicyDeclared|SITE-1|0
```

### Seed the local copies

Normally Kafka fills the layout and the task pool. For a local walk-through you
can insert them by hand (columns as in the migration); this is a development
shortcut, not a supported interface:

```sql
INSERT INTO zones(zone_id, site_code, zone_code) VALUES ('z1', 'SITE-1', 'A');
INSERT INTO locations(location_code, zone_id, x_m, y_m, z_m)
  VALUES ('L0','z1',0,0,0), ('A','z1',3,0,0), ('B','z1',3,4,0), ('D','z1',30,0,0);
INSERT INTO task_pool(task_id, task_type, site_id, from_location, to_location, state)
  VALUES ('T-1','PICK','SITE-1','A','A','AVAILABLE'),
         ('T-2','PICK','SITE-1','B','B','AVAILABLE'),
         ('T-far','PICK','SITE-1','D','D','AVAILABLE');
```

In Postgres mode `POST /recommendations` requires an `Idempotency-Key`:

```bash
curl -s -X POST localhost:18080/recommendations -H 'Content-Type: application/json' \
  -d '{"stationId":"STN-1","fromLocation":"L0"}'
# 400 idempotency-key-required: "send an Idempotency-Key header with a unique value per logical request"

curl -s -X POST localhost:18080/recommendations -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: demo-key-1' -d '{"stationId":"STN-1","fromLocation":"L0"}'
```

```json
{"stationId":"STN-1","siteId":"SITE-1","originLocation":"L0","policyVersion":1,"candidates":[{"rank":1,"taskId":"T-1","taskType":"PICK","location":"A","detourM":3},{"rank":2,"taskId":"T-2","taskType":"PICK","location":"B","detourM":7}]}
```

`T-far` is 30 m away, over `maxDetourM` 25, so it is not a candidate. Repeating
the request with the same key replays the stored answer byte for byte; the same
key with another body (`"fromLocation":"B"`) is `422 idempotency-key-reused`;
`idempotency_keys` then holds `demo-key-1` with status 200. A simulation changes
nothing: no hint, no outbox row.

Stop the service with Ctrl-C and remove the container with `docker rm -f ti-pg`.

## Wire Kafka (optional)

The relay publishes the outbox and the consumers fill the copies. Point them at
the fleet broker and choose stable group ids (the code has none):

```bash
export KAFKA_BROKERS=localhost:9092
export FULFILLMENT_MODE=kafka FULFILLMENT_CONSUMER_GROUP=task-interleaving-fulfillment-dev
export LAYOUT_MODE=kafka      LAYOUT_CONSUMER_GROUP=task-interleaving-layout-dev
```

With `DATABASE_URL` set the log adds `outbox relay running` and one `consumer
running` line per consumer (`topic`, `group_id`, `brokers`). A fresh group
replays its topic from the first offset, so the layout copy converges by itself.
Create the two topics this service produces (`warehouse.task-interleaving.events`
and `warehouse.task-interleaving.analytics`) first; see the
[Runbook](/docs/operations/runbook#kafka). A mode set to `kafka` without its group,
or without `KAFKA_BROKERS`, refuses to boot (messages in
[Troubleshooting](/docs/operations/troubleshooting)).

In a live cluster the pool only contains what fulfillment-execution announces:
`TaskAvailable` is published today only for RELOCATE and REPLENISH tasks (see
[Integration](/docs/ecosystem/integration)).

## Drive the domain by hand

The domain packages can still be exercised without any server. Create a
throwaway program **inside the module** (the packages are under `internal/`); a
directory starting with `_` is ignored by `./...`, so it does not disturb
`make build`. Delete it when you are done.

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

Output of a real run of exactly this program on this commit:

```text
rank 1 task-9c2e PICK at WH1-FWD-A01-02-01-A detour 4.0 m
rank 2 task-9d10 REPLENISH at WH1-FWD-A02-01-01-A detour 9.8 m
claim of a non-candidate consumes: false
claim of rank 1 consumes: true
event HintIssued (station version 2)
event HintConsumed (station version 3)
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
