-- 0008: account erasure (docs/domain/10-identity.md "ErasureRequest").
--
-- A subscriber can erase their own account (POST /v1/account/delete). The
-- erasure deletes every row identity holds for them, in one transaction, and
-- leaves two things.
--
-- identity_erasures: THE RECORD OF THE ERASURE, LEGAL_HOLD. "The record of an
-- erasure must outlive the erasure" (ErasureRequest); ADR-0012 classes erasure
-- audit as LEGAL_HOLD. It holds the subscriber id (usr_ + 32 hex, the keyed
-- HMAC already used as the subject everywhere; never the number), when the
-- erasure was requested and completed, and the exceptions: what it
-- deliberately left, and why. APPEND-ONLY, like identity_security_audit:
-- removing a record is a privileged operator action, not this service's.
--
-- identity_erasure_receipts: SHORT-LIVED. Lets a client that did not see the
-- answer retry and be told "done": the erased session's id and its device key
-- thumbprint, until the session would have expired. Housekeeping deletes it
-- after that, like the session itself.
--
-- FORWARD ONLY, like 0001-0007.

CREATE TABLE identity_erasures (
    id             BIGSERIAL   PRIMARY KEY,
    subscriber_id  TEXT        NOT NULL CHECK (subscriber_id ~ '^usr_[0-9a-f]{32}$'),
    requested_at   TIMESTAMPTZ NOT NULL,
    completed_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    exceptions     TEXT[]      NOT NULL
);

CREATE INDEX identity_erasures_subscriber ON identity_erasures (subscriber_id);

CREATE FUNCTION identity_erasures_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'identity_erasures is append-only (LEGAL_HOLD retention)'
        USING ERRCODE = 'CSA02';
END;
$$;

CREATE TRIGGER identity_erasures_no_change
    BEFORE UPDATE OR DELETE ON identity_erasures
    FOR EACH ROW EXECUTE FUNCTION identity_erasures_append_only();

CREATE TRIGGER identity_erasures_no_truncate
    BEFORE TRUNCATE ON identity_erasures
    FOR EACH STATEMENT EXECUTE FUNCTION identity_erasures_append_only();

CREATE TABLE identity_erasure_receipts (
    session_id             TEXT        PRIMARY KEY CHECK (session_id ~ '^sess_[0-9a-f]{64}$'),
    device_key_thumbprint  TEXT        NOT NULL CHECK (device_key_thumbprint <> ''),
    expires_at             TIMESTAMPTZ NOT NULL
);

CREATE INDEX identity_erasure_receipts_expires_at ON identity_erasure_receipts (expires_at);
