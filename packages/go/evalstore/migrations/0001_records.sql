-- 0001: the evaluation record store.
--
-- One table. The Repository port stores an opaque envelope and indexes it; it
-- never decodes the payload, so there is nothing here to normalise into further
-- tables and doing so would couple the schema to the platform's domain types.
--
-- The primary key is (kind, id) because that is exactly RecordKey. A surrogate
-- key would let two rows share an identity the port says is unique.

CREATE TABLE IF NOT EXISTS eval_records (
    kind        TEXT        NOT NULL,
    id          TEXT        NOT NULL,

    -- The payload's encoding version, not the database schema version. A record
    -- below CurrentSchema is migrated by Repository.Migrate, which is a
    -- different mechanism from the DDL chain this file belongs to.
    schema_ver  INTEGER     NOT NULL,

    scenario    TEXT        NOT NULL DEFAULT '',
    subject     TEXT        NOT NULL DEFAULT '',
    suite       TEXT        NOT NULL DEFAULT '',
    label       TEXT        NOT NULL DEFAULT '',

    -- The retention clock starts at CreatedAt, not at insertion: a record
    -- backfilled from an export must age from when it happened.
    created_at  TIMESTAMPTZ NOT NULL,

    -- NULL means "never expires", which is what a zero time.Time means on the
    -- Go side. Encoded as NULL rather than a sentinel timestamp so that
    -- "expires_at <= now" can never accidentally match it.
    expires_at  TIMESTAMPTZ,

    legal_hold  BOOLEAN     NOT NULL DEFAULT FALSE,

    -- BYTEA, not JSONB: the port documents the payload as OPAQUE. Storing it as
    -- JSONB would both assume an encoding the port does not promise and
    -- normalise the bytes, so a round trip would not return what was written.
    payload     BYTEA       NOT NULL,

    PRIMARY KEY (kind, id)
);

-- List orders by created_at DESC and filters on the index columns.
CREATE INDEX IF NOT EXISTS idx_eval_records_created_at
    ON eval_records (created_at DESC);

CREATE INDEX IF NOT EXISTS idx_eval_records_scenario
    ON eval_records (scenario) WHERE scenario <> '';

CREATE INDEX IF NOT EXISTS idx_eval_records_subject
    ON eval_records (subject) WHERE subject <> '';

CREATE INDEX IF NOT EXISTS idx_eval_records_suite
    ON eval_records (suite) WHERE suite <> '';

-- The sweep asks for expired, unheld, non-audit rows. A partial index keeps it
-- from scanning records that can never be swept.
CREATE INDEX IF NOT EXISTS idx_eval_records_sweepable
    ON eval_records (expires_at)
    WHERE expires_at IS NOT NULL AND legal_hold = FALSE AND kind <> 'audit';
