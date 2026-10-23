-- 0002: refresh tokens are stored only as keyed hashes.
--
-- identity_sessions.refresh_token (raw bearer tokens) becomes
-- refresh_token_hash, holding app.HashRefreshToken values: HMAC-SHA-256 under
-- the identity HMAC key, 64 lowercase hex characters.
--
-- EXISTING ROWS ARE REFUSED, NOT CONVERTED. Their column holds raw tokens, and
-- the database does not have the HMAC key, so it cannot compute their hashes.
-- Renaming the column over them would leave raw tokens in place under a name
-- that says "hash" — still usable by anyone who reads the table. So if any
-- session exists, this migration raises SQLSTATE CSI03 and, because the
-- migration runner applies each step in one transaction, the schema stays at
-- version 1 with nothing changed.
--
-- The controlled procedure is an explicit operator decision, not something this
-- file does: stop every identity instance, delete the sessions (which signs
-- every device out; they re-enrol through OTP), then re-run `service migrate`:
--
--     DELETE FROM identity_sessions;
--
-- FORWARD ONLY, like 0001.

-- Nothing may write a session between the check and the change.
LOCK TABLE identity_sessions IN ACCESS EXCLUSIVE MODE;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM identity_sessions) THEN
        RAISE EXCEPTION 'identity_sessions holds raw refresh tokens that cannot be hashed without the HMAC key'
            USING ERRCODE = 'CSI03',
                  HINT = 'Stop identity, remove every identity_sessions row (all devices re-enrol), then re-run the migration.';
    END IF;
END;
$$;

ALTER TABLE identity_sessions RENAME COLUMN refresh_token TO refresh_token_hash;

ALTER TABLE identity_sessions
    ADD CONSTRAINT identity_sessions_refresh_token_hash_canonical
    CHECK (refresh_token_hash ~ '^[0-9a-f]{64}$');
