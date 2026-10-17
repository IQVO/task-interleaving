-- 0001_interleaving_schema.up.sql: task-interleaving's OLTP schema (ADR 0005).
--
-- Text keys that are compared or ordered in Go (site ids, station ids, hint
-- ids, task ids, location codes) use the "C" collation so ORDER BY and keyset
-- comparisons follow byte order, the same order the in-memory adapters use.

-- interleaving_policies: the InterleavingPolicy of one site. version starts at 1
-- and guards every UPDATE (... WHERE version = <loaded>). A site without a row
-- has declared nothing and is inert. hint_ttl_ns keeps the time.Duration exactly.
CREATE TABLE interleaving_policies (
    site_id             TEXT COLLATE "C" PRIMARY KEY,
    max_detour_m        DOUBLE PRECISION NOT NULL CHECK (max_detour_m > 0),
    max_candidates      INTEGER          NOT NULL CHECK (max_candidates BETWEEN 1 AND 5),
    hint_ttl_ns         BIGINT           NOT NULL CHECK (hint_ttl_ns > 0),
    eligible_task_types TEXT[]           NOT NULL CHECK (cardinality(eligible_task_types) >= 1),
    vertical_weight     DOUBLE PRECISION NOT NULL CHECK (vertical_weight >= 0),
    version             BIGINT           NOT NULL CHECK (version >= 1),
    declared_at         TIMESTAMPTZ      NOT NULL
);

-- station_trips: one row per StationTrip aggregate. The position is the end of
-- the last completed task; a station that recorded nothing yet has no
-- last_completed_at, an empty site and version 0 (its row is written only
-- together with its first completion, so version >= 1 in practice). The
-- position point is all-or-nothing. version guards every UPDATE.
-- current_hint_id is the most recent hint in any state (the aggregate holds
-- exactly one); the history lives in hints.
CREATE TABLE station_trips (
    station_id        TEXT COLLATE "C" PRIMARY KEY,
    site_id           TEXT COLLATE "C" NOT NULL DEFAULT '',
    location_code     TEXT COLLATE "C" NOT NULL DEFAULT '',
    pos_x             DOUBLE PRECISION,
    pos_y             DOUBLE PRECISION,
    pos_z             DOUBLE PRECISION,
    last_task_id      TEXT COLLATE "C" NOT NULL DEFAULT '',
    last_completed_at TIMESTAMPTZ,
    current_hint_id   TEXT COLLATE "C",
    version           BIGINT      NOT NULL CHECK (version >= 0),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((pos_x IS NULL) = (pos_y IS NULL) AND (pos_x IS NULL) = (pos_z IS NULL))
);

-- hints: the hint history, one row per hint ever issued, queryable by state.
-- version starts at 1 and grows by one per state change. superseded_at is the
-- issue instant of the newer hint that replaced it.
CREATE TABLE hints (
    hint_id          TEXT COLLATE "C" PRIMARY KEY,
    station_id       TEXT COLLATE "C" NOT NULL REFERENCES station_trips (station_id),
    site_id          TEXT COLLATE "C" NOT NULL,
    after_task_id    TEXT COLLATE "C" NOT NULL,
    origin_location  TEXT COLLATE "C" NOT NULL,
    issued_at        TIMESTAMPTZ NOT NULL,
    expires_at       TIMESTAMPTZ NOT NULL,
    policy_version   BIGINT      NOT NULL CHECK (policy_version >= 1),
    state            TEXT        NOT NULL CHECK (state IN ('Active', 'Consumed', 'Expired', 'Superseded')),
    consumed_task_id TEXT COLLATE "C",
    consumed_at      TIMESTAMPTZ,
    superseded_at    TIMESTAMPTZ,
    version          BIGINT      NOT NULL CHECK (version >= 1),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (expires_at > issued_at),
    CHECK ((state = 'Consumed') = (consumed_task_id IS NOT NULL AND consumed_at IS NOT NULL)),
    CHECK (state = 'Consumed' OR (consumed_task_id IS NULL AND consumed_at IS NULL))
);

-- Invariant 2 (ADR 0002): at most ONE Active hint per station. The aggregate
-- supersedes the previous hint in the same step as it issues the next, so the
-- version guard on station_trips serialises the writers of one station; this
-- index is the database's own guarantee behind it.
CREATE UNIQUE INDEX uq_hints_one_active_per_station
    ON hints (station_id)
    WHERE state = 'Active';

-- The expiry sweeper reads the Active hints that are due.
CREATE INDEX idx_hints_active_expiry
    ON hints (expires_at)
    WHERE state = 'Active';

-- GET /hints: newest first, keyset paged on (issued_at, hint_id), narrowed by
-- state, station or site.
CREATE INDEX idx_hints_listing ON hints (issued_at DESC, hint_id DESC);
CREATE INDEX idx_hints_by_state ON hints (state, issued_at DESC, hint_id DESC);
CREATE INDEX idx_hints_by_station ON hints (station_id, issued_at DESC, hint_id DESC);
CREATE INDEX idx_hints_by_site ON hints (site_id, issued_at DESC, hint_id DESC);

-- station_trips.current_hint_id points at hints, hints.station_id at
-- station_trips: the pointer is checked at commit so a trip and its new hint
-- are written in one transaction in either order.
ALTER TABLE station_trips
    ADD CONSTRAINT station_trips_current_hint_fk
    FOREIGN KEY (current_hint_id) REFERENCES hints (hint_id)
    DEFERRABLE INITIALLY DEFERRED;

-- candidates: the ranked tasks a hint lists (immutable once the hint exists).
-- rank starts at 1; a task appears at most once per hint.
CREATE TABLE candidates (
    hint_id   TEXT COLLATE "C" NOT NULL REFERENCES hints (hint_id) ON DELETE CASCADE,
    rank      INTEGER          NOT NULL CHECK (rank >= 1),
    task_id   TEXT COLLATE "C" NOT NULL,
    task_type TEXT             NOT NULL,
    location  TEXT COLLATE "C" NOT NULL,
    detour_m  DOUBLE PRECISION NOT NULL CHECK (detour_m >= 0),
    PRIMARY KEY (hint_id, rank),
    CONSTRAINT candidates_hint_task_key UNIQUE (hint_id, task_id)
);

-- task_pool: the task pool copy (ADR 0003), one row per task, keyed by task id.
-- A task learned only from a claim or a completion is minimal: its site and
-- locations are empty. state: AVAILABLE | CLAIMED | COMPLETED.
CREATE TABLE task_pool (
    task_id               TEXT COLLATE "C" PRIMARY KEY,
    task_type             TEXT        NOT NULL DEFAULT '',
    site_id               TEXT COLLATE "C" NOT NULL DEFAULT '',
    from_location         TEXT COLLATE "C" NOT NULL DEFAULT '',
    to_location           TEXT COLLATE "C" NOT NULL DEFAULT '',
    required_capabilities TEXT[]      NOT NULL DEFAULT '{}',
    priority_band         TEXT        NOT NULL DEFAULT '',
    state                 TEXT        NOT NULL CHECK (state IN ('AVAILABLE', 'CLAIMED', 'COMPLETED')),
    claimed_by            TEXT        NOT NULL DEFAULT '',
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The recommender reads the AVAILABLE tasks of one site, in task id order.
CREATE INDEX idx_task_pool_available
    ON task_pool (site_id, task_id)
    WHERE state = 'AVAILABLE';

-- zones and locations: the layout copy (ADR 0003). A location joins its zone
-- through zone_id at read time, so the two may arrive in any order. The
-- geometry point sits on the location row: it may arrive before the
-- registration (empty zone_id) and is all-or-nothing. decommissioned is
-- irreversible and the row stays, so a late registration cannot bring the
-- location back.
CREATE TABLE zones (
    zone_id    TEXT PRIMARY KEY,
    site_code  TEXT        NOT NULL,
    zone_code  TEXT        NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE locations (
    location_code  TEXT COLLATE "C" PRIMARY KEY,
    zone_id        TEXT             NOT NULL DEFAULT '',
    decommissioned BOOLEAN          NOT NULL DEFAULT false,
    x_m            DOUBLE PRECISION,
    y_m            DOUBLE PRECISION,
    z_m            DOUBLE PRECISION,
    updated_at     TIMESTAMPTZ      NOT NULL DEFAULT now(),
    CHECK ((x_m IS NULL) = (y_m IS NULL) AND (x_m IS NULL) = (z_m IS NULL))
);

CREATE INDEX idx_locations_zone ON locations (zone_id);

-- processed_events: idempotency guard of the inbound consumers. (consumer,
-- event_id) is claimed with INSERT ... ON CONFLICT DO NOTHING in the SAME
-- transaction as the effect, so a rollback un-claims it.
CREATE TABLE processed_events (
    consumer     TEXT        NOT NULL,
    event_id     TEXT        NOT NULL,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (consumer, event_id)
);

-- outbox_events: the transactional outbox (same shape as the fleet's). One row
-- per already-encoded Kafka message, inserted in the SAME transaction as the
-- aggregate; the relay drains unpublished rows to Kafka.
--   event_id    the CloudEvents `id`, minted once at encode time and persisted:
--               a relay retry republishes the same id.
--   event_type  the FULL CloudEvents `type`.
--   subject     the CloudEvents `subject`.
--   key         the Kafka message key.
--   dataschema  the CloudEvents `dataschema` URN.
--   value       the structured-mode CloudEvents JSON bytes.
--   headers     [{"key":..,"value":..}] Kafka headers (content-type, trace).
CREATE TABLE outbox_events (
    id           BIGSERIAL   PRIMARY KEY,
    event_id     TEXT        NOT NULL,
    topic        TEXT        NOT NULL,
    event_type   TEXT        NOT NULL,
    subject      TEXT        NOT NULL,
    key          BYTEA,
    dataschema   TEXT        NOT NULL,
    value        BYTEA       NOT NULL,
    headers      JSONB       NOT NULL DEFAULT '[]',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ,
    attempts     INTEGER     NOT NULL DEFAULT 0,
    last_error   TEXT,
    CONSTRAINT outbox_events_event_id_topic_key UNIQUE (event_id, topic)
);

-- The relay only ever scans the unpublished tail; keep that scan tiny.
CREATE INDEX idx_outbox_events_unpublished ON outbox_events (id) WHERE published_at IS NULL;

-- idempotency_keys: the answers of POSTs made with an Idempotency-Key. Only 2xx
-- and 4xx answers are stored, never 5xx; the first writer of a key wins.
-- fingerprint identifies the request body so the same key with another body
-- can be told apart.
CREATE TABLE idempotency_keys (
    key          TEXT PRIMARY KEY,
    fingerprint  TEXT        NOT NULL,
    status       INTEGER     NOT NULL CHECK (status >= 200 AND status < 500),
    content_type TEXT        NOT NULL DEFAULT '',
    body         BYTEA       NOT NULL DEFAULT ''::bytea,
    created_at   TIMESTAMPTZ NOT NULL
);

CREATE INDEX idx_idempotency_keys_created_at ON idempotency_keys (created_at);
