-- 0012: sign out of every device (POST /v1/session/revoke-all; OD-14 option
-- B, ADR-0035 D35.9).
--
-- A revoke-all revokes every session of an account in one transaction, and
-- records the operation here, in the same transaction, by the session that
-- asked. The record is what makes a retry safe: a client that did not see the
-- answer retries with the same session and refresh token and a fresh proof,
-- and is told "done" WITHOUT revoking again, so a session signed in since the
-- operation is never touched. A new operation, from a session still live,
-- records its own row and revokes every session active at that moment.
--
-- ONE ROW PER OPERATION. Revoke-all revokes the session that asked, and a
-- revoked session's refresh token never rotates again (the swap refuses a
-- revoked row, and 0001's trigger refuses un-revoking), so the session id
-- names at most one completed operation.
--
-- ONLY KEYED HASHES. Like identity_sessions.refresh_token_hash (0002), the
-- row holds HMAC-SHA-256(identity key, token), never a raw token, and the
-- CHECK refuses anything that is not a canonical hash. No account id, no
-- number, no email, no device id.
--
-- BOUNDED BY THE SESSION. The row is deleted with its session (ON DELETE
-- CASCADE): by housekeeping after the session expires (at most 24 hours), and
-- by account erasure. It needs no housekeeping of its own.
--
-- FORWARD ONLY, like 0001-0011. It creates one table and touches no existing
-- table or row.
CREATE TABLE identity_session_revocations (
    session_id          TEXT        PRIMARY KEY
        REFERENCES identity_sessions (session_id) ON DELETE CASCADE,
    refresh_token_hash  TEXT        NOT NULL
        CONSTRAINT identity_session_revocations_hash_canonical
        CHECK (refresh_token_hash ~ '^[0-9a-f]{64}$'),
    revoked_at          TIMESTAMPTZ NOT NULL,
    sessions_revoked    INTEGER     NOT NULL CHECK (sessions_revoked >= 1)
);
