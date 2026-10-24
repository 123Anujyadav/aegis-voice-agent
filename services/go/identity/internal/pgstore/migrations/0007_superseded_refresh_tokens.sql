-- 0007: the refresh tokens each session has rotated away from.
--
-- Reuse detection (RFC 9700 §4.14.2) revokes a session when a refresh token it
-- once held is presented again: two parties hold the token family. Until now
-- any token that was not the current one counted as reuse, so anyone who knew
-- a session id could revoke the session by sending any string
-- (docs/phase19/REFRESH-TOKEN-THREAT-ANALYSIS.md §3). Recording the tokens a
-- session actually held lets identity tell reuse from a guess: reuse revokes,
-- a guess is refused and revokes nothing.
--
-- ONLY KEYED HASHES. Like identity_sessions.refresh_token_hash (0002), a row
-- holds HMAC-SHA-256(identity key, token), never a raw token, and the CHECK
-- refuses anything that is not a canonical hash.
--
-- BOUNDED BY THE SESSION. Rows are written by the rotation's compare-and-swap,
-- one per refresh, and deleted with their session (ON DELETE CASCADE), which
-- housekeeping removes after it expires: at most a session lifetime of rows
-- per session. They need no key id of their own (ADR-0030 D30.2): they never
-- outlive the session whose current hash would be re-keyed.
--
-- FORWARD ONLY, like 0001-0006.
CREATE TABLE identity_refresh_tokens_superseded (
    session_id          TEXT        NOT NULL
        REFERENCES identity_sessions (session_id) ON DELETE CASCADE,
    refresh_token_hash  TEXT        NOT NULL
        CONSTRAINT identity_refresh_tokens_superseded_hash_canonical
        CHECK (refresh_token_hash ~ '^[0-9a-f]{64}$'),
    superseded_at       TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (session_id, refresh_token_hash)
);
