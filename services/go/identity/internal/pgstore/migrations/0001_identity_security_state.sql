-- 0001: identity security state.
--
-- Everything app.IdentityRepository and dpop.ReplayStore hold, and nothing
-- else. Every table is prefixed identity_ so this chain cannot collide with
-- another service's tables in a shared database, and nothing here touches
-- packages/go/persistence/schema.sql.
--
-- FORWARD ONLY. There is no down-migration: dropping these tables discards
-- sessions, lockouts and replay reservations, which is a security event, not a
-- rollback. Roll back by restoring a snapshot taken before migrating.

-- OTP challenges. The code is stored only as the service's HMAC (code_hash);
-- the code itself never reaches the repository.
CREATE TABLE identity_otp_challenges (
    request_id  TEXT        PRIMARY KEY,
    msisdn      TEXT        NOT NULL,
    code_hash   TEXT        NOT NULL,
    attempts    INTEGER     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    expires_at  TIMESTAMPTZ NOT NULL
);

-- For a future sweep of expired challenges; nothing deletes them by age yet.
CREATE INDEX identity_otp_challenges_expires_at ON identity_otp_challenges (expires_at);

-- One row per MSISDN that has rate-limit state. Every rate-limit operation
-- locks this row (SELECT ... FOR UPDATE) for its whole transaction, which is
-- what serialises concurrent requests for one number across processes.
CREATE TABLE identity_otp_rate_state (
    msisdn        TEXT        PRIMARY KEY,
    -- NULL means no lockout has been recorded. A past value is a lockout that
    -- has ended; it is cleared by a success or by releasing its reservation.
    locked_until  TIMESTAMPTZ,
    -- The verification reservation that set the lockout.
    locked_by     BIGINT
);

-- OTP requests inside the rolling window. Rows older than the window are
-- deleted by the next request for the same number.
CREATE TABLE identity_otp_requests (
    id            BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    msisdn        TEXT        NOT NULL REFERENCES identity_otp_rate_state (msisdn),
    requested_at  TIMESTAMPTZ NOT NULL
);

CREATE INDEX identity_otp_requests_msisdn_at ON identity_otp_requests (msisdn, requested_at);

-- Reservation ids are unique across every process sharing the database.
CREATE SEQUENCE identity_otp_reservation_seq AS BIGINT START WITH 1;

-- Counted and provisional failed verifications inside the rolling window.
CREATE TABLE identity_otp_failures (
    reservation_id  BIGINT      PRIMARY KEY,
    msisdn          TEXT        NOT NULL REFERENCES identity_otp_rate_state (msisdn),
    failed_at       TIMESTAMPTZ NOT NULL
);

CREATE INDEX identity_otp_failures_msisdn_at ON identity_otp_failures (msisdn, failed_at);

-- Sessions. One MSISDN may hold many sessions (one per sign-in), so msisdn is
-- NOT unique here: the global-uniqueness rule applies to subscriber records,
-- which this table is not.
--
-- refresh_token is stored as issued, because IdentityRepository returns it and
-- the service compares it; storing only a hash needs a contract change (see the
-- package documentation). No access token is ever stored.
CREATE TABLE identity_sessions (
    session_id             TEXT        PRIMARY KEY,
    subscriber_id          TEXT        NOT NULL,
    msisdn                 TEXT        NOT NULL,
    refresh_token          TEXT        NOT NULL,
    device_id              TEXT        NOT NULL DEFAULT '',
    -- NULL is a nil slice; '{}' is an empty one. Both round-trip.
    roles                  TEXT[],
    created_at             TIMESTAMPTZ NOT NULL,
    expires_at             TIMESTAMPTZ NOT NULL,
    is_revoked             BOOLEAN     NOT NULL DEFAULT FALSE,
    -- '' means not enrolled.
    device_key_thumbprint  TEXT        NOT NULL DEFAULT ''
);

CREATE INDEX identity_sessions_subscriber ON identity_sessions (subscriber_id);
CREATE INDEX identity_sessions_expires_at ON identity_sessions (expires_at);

-- The database itself refuses, whatever the writer, to change an enrolled
-- device key or to clear a revocation. The repository never attempts either;
-- these are the backstop for any other writer. The SQLSTATEs are this
-- service's own (class CS) and are mapped back to the app sentinels.
CREATE FUNCTION identity_sessions_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.device_key_thumbprint <> '' AND NEW.device_key_thumbprint IS DISTINCT FROM OLD.device_key_thumbprint THEN
        RAISE EXCEPTION 'enrolled device key cannot change' USING ERRCODE = 'CSI01';
    END IF;
    IF OLD.is_revoked AND NOT NEW.is_revoked THEN
        RAISE EXCEPTION 'revocation cannot be undone' USING ERRCODE = 'CSI02';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER identity_sessions_guard
    BEFORE UPDATE ON identity_sessions
    FOR EACH ROW EXECUTE FUNCTION identity_sessions_guard();

-- Device attestations, as SaveDevice stores them.
CREATE TABLE identity_device_attestations (
    device_id          TEXT        PRIMARY KEY,
    model              TEXT        NOT NULL,
    os_version         TEXT        NOT NULL,
    integrity_token    TEXT        NOT NULL,
    attested_at        TIMESTAMPTZ NOT NULL,
    is_play_certified  BOOLEAN     NOT NULL
);

-- Guardian delegations. The repository contract only reads these; nothing in
-- the identity service writes them yet.
CREATE TABLE identity_guardian_delegations (
    guardian_id    TEXT        NOT NULL,
    subscriber_id  TEXT        NOT NULL,
    permissions    TEXT[],
    granted_at     TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (guardian_id, subscriber_id)
);

-- DPoP proof reservations, shared by every process using this database. The
-- primary key is the replay check: a second (jkt, jti) cannot be inserted
-- while the first is unexpired.
CREATE TABLE identity_dpop_replay (
    jkt         TEXT        NOT NULL CHECK (jkt <> ''),
    jti         TEXT        NOT NULL CHECK (jti <> ''),
    expires_at  TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (jkt, jti)
);

CREATE INDEX identity_dpop_replay_expires_at ON identity_dpop_replay (expires_at);
