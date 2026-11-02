---
id: quickstart
title: Quickstart
sidebar_label: Quickstart
sidebar_position: 3
---

# Quickstart

Build the service, boot it (and see what it refuses to boot without), run it on
Postgres and Kafka and make the first calls. The boot refusals, the readiness
answer and the policy call below were captured from real runs of `cmd/api`
built from this commit; the other REST answers were captured from an earlier
run of the same API (the REST surface did not change).

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
through the kind cluster's external access); the docker-compose Kafka is retired
for the fleet. The service needs Postgres **and** Kafka to boot and to be ready:
there is no mode without them ([ADR 0006](/docs/adr/0006-postgres-and-kafka-are-the-only-runtime)).
`make run-local` starts a throwaway Postgres and Kafka with `docker-compose.yml`
(see the note in the README: that file is not verified end to end).

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

Build the binary and start it with nothing configured:

```bash
go build -o task-interleaving ./cmd/api
HTTP_ADDR=127.0.0.1:18080 ./task-interleaving
```

It refuses to boot, before it opens a connection or binds the port, and names
every mandatory variable that is missing (exit status 1; real output, `time`
omitted):

```text
{"level":"ERROR","msg":"service exited with error","error":"invalid configuration: DATABASE_URL is required: this context runs on real Postgres\nKAFKA_BROKERS is required: this context runs on real Kafka\nFULFILLMENT_CONSUMER_GROUP is required: this context runs on real Kafka\nLAYOUT_CONSUMER_GROUP is required: this context runs on real Kafka"}
```

`*_MODE=permissive` and a bad `HINT_EXPIRY_INTERVAL` are boot errors too:

```text
invalid configuration: FULFILLMENT_MODE=permissive is not supported: this context runs on real Kafka
HINT_EXPIRY_INTERVAL "soon" is not a valid duration (use a Go duration such as 5s or 500ms): time: invalid duration "soon"
```

## Run on Postgres and Kafka

Start a throwaway database (use your Kafka for the broker; the service never
creates topics through an admin API: the cluster broker auto-creates
`warehouse.task-interleaving.events` and `warehouse.task-interleaving.analytics`
on the relay's first write, and the consumed `warehouse.fulfillment.events` and
`warehouse.facility.events` exist once their producers published; a broker with
auto-creation off needs them created by hand):

```bash
docker run -d --rm --name ti-pg -e POSTGRES_PASSWORD=pw -e POSTGRES_DB=ti \
  -p 127.0.0.1:55432:5432 postgres:16-alpine

DATABASE_URL='postgres://postgres:pw@127.0.0.1:55432/ti?sslmode=disable' \
KAFKA_BROKERS=localhost:9092 \
FULFILLMENT_CONSUMER_GROUP=task-interleaving-fulfillment-dev \
LAYOUT_CONSUMER_GROUP=task-interleaving-layout-dev \
HTTP_ADDR=127.0.0.1:18080 ./task-interleaving
```

The group ids are yours to choose (the code has none); a fresh group replays its
topic from the first offset, so the layout copy converges by itself. Migrations
run at boot. The startup log (JSON lines on stdout, `time` omitted) is, in
order: `postgres adapters configured`, `kafka topics (informational; the broker
auto-creates them on first write, the service never creates them through an
admin API)` with the `brokers`, `consumes` and `produces`
lists, one `consumer running` line per consumer (`topic`, `group_id`,
`brokers`), `hint expiry worker running`, `outbox relay running` and `http
server listening`. The [Observability](/docs/operations/observability) page lists
every message.

`/readyz` is ready only while Postgres answers and a Kafka broker answers a
metadata request; topics are not checked, so it is `200` before any topic
exists. With the database up and the broker **unreachable** (a real run against
`KAFKA_BROKERS=127.0.0.1:1`) the process stays up and live, the consumers log
`kafka reader error; retrying` and the probe says what is down, never why:

```text
curl -si localhost:18080/readyz
HTTP/1.1 503 Service Unavailable
Content-Type: application/problem+json

{"type":"https://errors.task-interleaving.warehouse-systems.dev/dependency-unavailable","title":"A runtime dependency is unavailable","status":503,"detail":"not ready: kafka","instance":"/readyz"}

curl -s localhost:18080/healthz
{"status":"ok"}
```

(the cause, here `dial tcp 127.0.0.1:1: connect: connection refused`, is in the
WARN log `readiness: dependency is not ready`). Stop it with Ctrl-C; it flips
`/readyz` to 503, waits `SHUTDOWN_DRAIN_DELAY` (5 s), drains HTTP, stops the
consumers, the expiry worker and the relay, and closes the pool.

## First calls

REST is unauthenticated. Probes, then declare a policy:

```bash
curl -s localhost:18080/healthz    # {"status":"ok"}
curl -s localhost:18080/readyz     # {"status":"ready"} once Postgres and Kafka are up

curl -si -X PUT localhost:18080/sites/SITE-1/policy \
  -H 'Content-Type: application/json' \
  -d '{"maxDetourM":25,"maxCandidates":3,"hintTtlSeconds":90,"eligibleTaskTypes":["PICK","REPLENISH"],"verticalWeight":1.5}'
```

```text
200 OK, header ETag: "1", Content-Type: application/json
{"siteId":"SITE-1","maxDetourM":25,"maxCandidates":3,"hintTtlSeconds":90,"eligibleTaskTypes":["PICK","REPLENISH"],"verticalWeight":1.5,"version":1,"declaredAt":"2026-10-17T17:57:02.317337Z"}
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
| `POST /recommendations` `{"stationId":"STN-1"}` | `400` `idempotency-key-required` without the header; with it, `422` `station-position-unknown` ("send fromLocation") |
| `POST /recommendations` `{"stationId":"STN-1","fromLocation":"NOWHERE"}` (with a key) | `422` `origin-unknown` ("no geometry is known for location NOWHERE") |

All errors are `application/problem+json`, for example:

```json
{"type":"https://errors.task-interleaving.warehouse-systems.dev/policy-not-found","title":"Interleaving policy not found","status":404,"detail":"the site has not declared an interleaving policy","instance":"/sites/SITE-1/policy"}
```

Declare a policy, then look at the outbox:

```bash
curl -s -X PUT localhost:18080/sites/SITE-1/policy -H 'Content-Type: application/json' \
  -d '{"maxDetourM":25,"maxCandidates":3,"hintTtlSeconds":90,"eligibleTaskTypes":["PICK"]}'
docker exec ti-pg psql -U postgres -d ti -At \
  -c "select id,topic,event_type,subject,attempts from outbox_events"
# 1|warehouse.task-interleaving.analytics|com.warehouse.wes.task-interleaving.policy.InterleavingPolicyDeclared|SITE-1|0
```

The relay publishes that row to Kafka within `OUTBOX_RELAY_INTERVAL` (1 s by
default) and sets `published_at`. While Kafka is unreachable `attempts` grows by
one per pass and the row is parked at `OUTBOX_MAX_ATTEMPTS` (20); see the
[Runbook](/docs/operations/runbook#kafka).

`schema_migrations` reads `version 1, dirty f` and the public schema holds
`candidates, hints, idempotency_keys, interleaving_policies, locations,
outbox_events, processed_events, schema_migrations, station_trips, task_pool, zones`.

### Seed the local copies

Normally Kafka fills the layout and the task pool: produce `ZoneRegistered`,
`LocationSlotRegistered` and `LocationGeometryUpdated` to
`warehouse.facility.events` and `TaskAvailable` to `warehouse.fulfillment.events`
(the end-to-end test `cmd/api/e2e_integration_test.go` does exactly that, builds
the CloudEvents and checks the hint that results). For a quick local look you
can instead insert the copies by hand (columns as in the migration); this is a
development shortcut, not a supported interface:

```sql
INSERT INTO zones(zone_id, site_code, zone_code) VALUES ('z1', 'SITE-1', 'A');
INSERT INTO locations(location_code, zone_id, x_m, y_m, z_m)
  VALUES ('L0','z1',0,0,0), ('A','z1',3,0,0), ('B','z1',3,4,0), ('D','z1',30,0,0);
INSERT INTO task_pool(task_id, task_type, site_id, from_location, to_location, state)
  VALUES ('T-1','PICK','SITE-1','A','A','AVAILABLE'),
         ('T-2','PICK','SITE-1','B','B','AVAILABLE'),
         ('T-far','PICK','SITE-1','D','D','AVAILABLE');
```

`POST /recommendations` requires an `Idempotency-Key` in every deployment:

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
the request with the same key replays the stored answer byte for byte, marked
with the response header `Idempotency-Replayed: true`; the same
key with another body (`"fromLocation":"B"`) is `409 idempotency-key-reused`;
`idempotency_keys` then holds `demo-key-1` with status 200. A simulation changes
nothing: no hint, no outbox row.

Stop the service with Ctrl-C and remove the container with `docker rm -f ti-pg`.

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
