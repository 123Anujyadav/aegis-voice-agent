-- 0005: a durable, append-only security audit log.
--
-- ADR-0010 §10 requires audit records for enrolment, refresh-token reuse
-- detection and OTP abuse; ADR-0012 §5.3 classes "Audit logs (auth, access,
-- erasure)" as PERSONAL with LEGAL_HOLD retention: never deleted by a
-- schedule. Until now identity only counted these events.
--
-- APPEND-ONLY. A trigger refuses every UPDATE, DELETE and TRUNCATE, so neither
-- the application nor housekeeping can alter or remove a record. Removing
-- records (for example, when a legal hold is lifted) is an explicit,
-- privileged operator action that must drop the trigger first, and is not
-- something this service does.
--
-- NO PERSONAL IDENTIFIER BEYOND RANDOM IDS. A record holds the closed event
-- type, when it happened, and the event's own random identifier: an OTP
-- challenge's request id (req_ + 64 hex) or a session id (sess_ + 64 hex).
-- The CHECK constraints make it impossible to store a phone number, a code, a
-- token or free text in these columns, whatever the application sends.
--
-- FORWARD ONLY, like 0001-0004.

CREATE TABLE identity_security_audit (
    id            BIGSERIAL   PRIMARY KEY,
    event_type    TEXT        NOT NULL CHECK (event_type ~ '^identity(\.[a-z_]+)+$' AND length(event_type) <= 64),
    occurred_at   TIMESTAMPTZ NOT NULL,
    request_id    TEXT        CHECK (request_id ~ '^req_[0-9a-f]{64}$'),
    session_id    TEXT        CHECK (session_id ~ '^sess_[0-9a-f]{64}$'),
    locked_until  TIMESTAMPTZ,
    recorded_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX identity_security_audit_occurred_at ON identity_security_audit (occurred_at);

CREATE FUNCTION identity_security_audit_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'identity_security_audit is append-only (LEGAL_HOLD retention)'
        USING ERRCODE = 'CSA01';
END;
$$;

CREATE TRIGGER identity_security_audit_no_change
    BEFORE UPDATE OR DELETE ON identity_security_audit
    FOR EACH ROW EXECUTE FUNCTION identity_security_audit_append_only();

CREATE TRIGGER identity_security_audit_no_truncate
    BEFORE TRUNCATE ON identity_security_audit
    FOR EACH STATEMENT EXECUTE FUNCTION identity_security_audit_append_only();
