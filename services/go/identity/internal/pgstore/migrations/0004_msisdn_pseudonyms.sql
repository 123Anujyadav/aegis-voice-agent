-- 0004: phone numbers are no longer stored.
--
-- OTP and rate-limit state is keyed by app.MSISDNPseudonym — HMAC-SHA-256 under
-- the identity HMAC key over a domain-separated canonical E.164 number, 64
-- lowercase hex characters — and sessions carry no number at all.
--
--   identity_otp_challenges.msisdn  -> msisdn_pseudonym, plus subscriber_id
--                                      (derived from the number when the
--                                      challenge is created)
--   identity_otp_rate_state.msisdn  -> msisdn_pseudonym (the primary key)
--   identity_otp_requests.msisdn    -> msisdn_pseudonym (foreign key follows)
--   identity_otp_failures.msisdn    -> msisdn_pseudonym (foreign key follows)
--   identity_sessions.msisdn        -> dropped (nothing read it)
--
-- Indexes and foreign keys on renamed columns follow the rename; their names
-- keep the word msisdn, which is only a name.
--
-- POPULATED OTP AND RATE-LIMIT TABLES ARE REFUSED, NOT CONVERTED. Their rows
-- hold raw numbers and the database does not have the HMAC key, so it cannot
-- compute pseudonyms. If any of the four holds a row, this raises SQLSTATE
-- CSI04 and, because each migration runs in one transaction, nothing changes.
--
-- The controlled procedure is an explicit operator decision, not something this
-- file does. Every row in these tables is short-lived (5-minute challenges, a
-- 15-minute rate window, a 15-minute lockout), so: stop every identity
-- instance, wait until no lockout can still be in force (at least the lockout
-- period after the last request), delete the rows of the four tables (children
-- first), then re-run `service migrate`. Users with a challenge in flight must
-- request a new code. Deleting while a lockout is in force would lift it.
--
-- Sessions are NOT refused: dropping their msisdn column removes the personal
-- data and nothing else; every session keeps working.
--
-- FORWARD ONLY. Rolling back means restoring a pre-migration snapshot; the
-- dropped session numbers are not recoverable from this schema.

LOCK TABLE identity_otp_challenges, identity_otp_failures, identity_otp_requests,
           identity_otp_rate_state, identity_sessions IN ACCESS EXCLUSIVE MODE;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM identity_otp_challenges)
       OR EXISTS (SELECT 1 FROM identity_otp_rate_state)
       OR EXISTS (SELECT 1 FROM identity_otp_requests)
       OR EXISTS (SELECT 1 FROM identity_otp_failures) THEN
        RAISE EXCEPTION 'OTP and rate-limit tables hold raw phone numbers that cannot be pseudonymised without the HMAC key'
            USING ERRCODE = 'CSI04',
                  HINT = 'Stop identity, wait out any lockout, remove the rows of the four OTP and rate-limit tables, then re-run the migration.';
    END IF;
END;
$$;

ALTER TABLE identity_otp_challenges RENAME COLUMN msisdn TO msisdn_pseudonym;
ALTER TABLE identity_otp_challenges ADD COLUMN subscriber_id TEXT NOT NULL;
ALTER TABLE identity_otp_rate_state RENAME COLUMN msisdn TO msisdn_pseudonym;
ALTER TABLE identity_otp_requests RENAME COLUMN msisdn TO msisdn_pseudonym;
ALTER TABLE identity_otp_failures RENAME COLUMN msisdn TO msisdn_pseudonym;

ALTER TABLE identity_otp_challenges
    ADD CONSTRAINT identity_otp_challenges_msisdn_pseudonym_canonical CHECK (msisdn_pseudonym ~ '^[0-9a-f]{64}$');
ALTER TABLE identity_otp_rate_state
    ADD CONSTRAINT identity_otp_rate_state_msisdn_pseudonym_canonical CHECK (msisdn_pseudonym ~ '^[0-9a-f]{64}$');
ALTER TABLE identity_otp_requests
    ADD CONSTRAINT identity_otp_requests_msisdn_pseudonym_canonical CHECK (msisdn_pseudonym ~ '^[0-9a-f]{64}$');
ALTER TABLE identity_otp_failures
    ADD CONSTRAINT identity_otp_failures_msisdn_pseudonym_canonical CHECK (msisdn_pseudonym ~ '^[0-9a-f]{64}$');

ALTER TABLE identity_sessions DROP COLUMN msisdn;
