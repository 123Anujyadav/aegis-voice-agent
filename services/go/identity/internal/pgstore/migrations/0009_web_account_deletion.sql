-- 0009: web account deletion (docs/phase19/ACCOUNT-DELETION.md §3,
-- internal/app/web_erasure.go).
--
-- identity_otp_challenges.purpose: a one-time code is accepted only for what
-- it was issued for. Every existing challenge is a sign-in challenge, which
-- the default records; a sign-in code cannot delete an account and a
-- deletion code cannot sign in.
--
-- identity_deletion_code_receipts: SHORT-LIVED, like identity_erasure_receipts.
-- Lets a browser that did not see the answer retry the confirmation and be
-- told "done": the random request id and the code's keyed hash, until the
-- challenge would have expired. Housekeeping deletes it after that.
--
-- identity_deletion_code_budget: the global hourly count of deletion-code
-- requests, so the deployment's cap holds across replicas. One row per hour,
-- holding a count and nothing else; housekeeping deletes old hours.
--
-- FORWARD ONLY, like 0001-0008. The ALTER adds a column with a constant
-- default and a CHECK every existing row satisfies; it changes no data.

ALTER TABLE identity_otp_challenges
    ADD COLUMN purpose TEXT NOT NULL DEFAULT 'sign_in'
        CHECK (purpose IN ('sign_in', 'account_deletion'));

CREATE TABLE identity_deletion_code_receipts (
    request_id  TEXT        PRIMARY KEY CHECK (request_id ~ '^req_[0-9a-f]{64}$'),
    code_hash   TEXT        NOT NULL CHECK (code_hash <> ''),
    expires_at  TIMESTAMPTZ NOT NULL
);

CREATE INDEX identity_deletion_code_receipts_expires_at ON identity_deletion_code_receipts (expires_at);

CREATE TABLE identity_deletion_code_budget (
    hour_start  TIMESTAMPTZ PRIMARY KEY,
    requests    INTEGER     NOT NULL CHECK (requests >= 0)
);
