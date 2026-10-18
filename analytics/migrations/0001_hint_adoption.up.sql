-- task-interleaving analytics read model (ADR 0008).
--
-- This is the ANALYTICAL database (task_interleaving_analytics), separate from
-- the OLTP database. It is written only by cmd/ti-projector and read
-- (read-only) by cmd/ti-reports. Everything here is a projection derived from
-- warehouse.task-interleaving.analytics, not a source of truth: it can be
-- dropped and rebuilt by replaying the topic under a new consumer group.
--
-- Facts, not aggregates: every per-day number of the hint-adoption report is
-- counted from these rows over the requested window. Each milestone column is
-- the CloudEvents time of the EARLIEST such event seen (kept with LEAST, which
-- ignores NULLs), so arrival order never matters.

-- Idempotency: every applied CloudEvents id is recorded here exactly once, in
-- the SAME transaction as its effect. occurred_at is the event's CloudEvents
-- `time`; max(occurred_at) is the freshness of the projection. The
-- InterleavingPolicyDeclared events are recorded here only (they feed the
-- freshness, no fact table).
CREATE TABLE analytics_processed_events (
    event_id    TEXT        PRIMARY KEY,
    event_type  TEXT        NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    applied_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_analytics_processed_events_occurred_at ON analytics_processed_events (occurred_at);

-- hint_facts: one row per hint the projection has seen (any of its events),
-- one column per milestone.
--   issued_at    InterleavingHintIssued   (expires_at, candidate_count)
--   consumed_at  InterleavingHintConsumed (consumed_rank, consumed_detour_m)
--   expired_at   InterleavingHintExpired
-- A hint superseded by a newer one has no event of its own: it stays without a
-- consumed_at/expired_at. Time to consume is consumed_at - issued_at and
-- exists only when the projection saw both.
CREATE TABLE hint_facts (
    hint_id           TEXT COLLATE "C" PRIMARY KEY,
    station_id        TEXT COLLATE "C" NOT NULL,
    site_id           TEXT COLLATE "C" NOT NULL,
    issued_at         TIMESTAMPTZ,
    expires_at        TIMESTAMPTZ,
    candidate_count   INTEGER,
    consumed_at       TIMESTAMPTZ,
    consumed_rank     INTEGER,
    consumed_detour_m DOUBLE PRECISION,
    expired_at        TIMESTAMPTZ
);

CREATE INDEX idx_hint_facts_site        ON hint_facts (site_id);
CREATE INDEX idx_hint_facts_issued_at   ON hint_facts (issued_at)   WHERE issued_at IS NOT NULL;
CREATE INDEX idx_hint_facts_consumed_at ON hint_facts (consumed_at) WHERE consumed_at IS NOT NULL;
CREATE INDEX idx_hint_facts_expired_at  ON hint_facts (expired_at)  WHERE expired_at IS NOT NULL;
