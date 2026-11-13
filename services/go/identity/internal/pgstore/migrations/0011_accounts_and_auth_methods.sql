-- 0011: accounts and their authentication methods (ADR-0035 D35.1, D35.2,
-- D35.8, D35.10; accepted for M1 on internal assurance, 2026-11-13; O2).
--
-- identity_accounts: the stored, random account id of every account created by
-- Google Sign-In (and, later, a passkey): "usr_" and 32 lowercase hex
-- characters from 128 bits of crypto/rand, never derived from any identifier.
-- Number-derived ids from the SMS route are NOT stored here and are unchanged.
-- An erased account's row stays, with status 'erased': the id is tombstoned and
-- never reused (INV-ID-7, option A).
--
-- identity_auth_methods: one row per authentication method attached to an
-- account. A method is found by its lookup pseudonym, an HMAC under a keyring
-- key used for nothing else (app/pseudonym_keyring.go); key_id names that key,
-- so the keyring can rotate. Among methods not revoked, a pseudonym belongs to
-- one account only. No email, name or token is stored. Erasure deletes an
-- account's methods.
--
-- identity_account_creation_budget: the global hourly cap on new accounts
-- (the M1 gate, D35.8), one row per clock hour holding a count; the same
-- upsert-with-guard as identity_signin_delivery_budget (0010).
--
-- identity_google_token_replay: the SHA-256 of every Google ID token already
-- redeemed, until the token's own expiry (D35.4: single use). Housekeeping
-- deletes expired rows.
--
-- FORWARD ONLY, like 0001-0010. It creates four new tables and touches no
-- existing table or row.

CREATE TABLE identity_accounts (
    account_id  TEXT        PRIMARY KEY CHECK (account_id ~ '^usr_[0-9a-f]{32}$'),
    status      TEXT        NOT NULL CHECK (status IN ('active', 'erased')),
    created_at  TIMESTAMPTZ NOT NULL,
    erased_at   TIMESTAMPTZ,
    CHECK ((status = 'erased') = (erased_at IS NOT NULL))
);

CREATE TABLE identity_auth_methods (
    method_id         TEXT        PRIMARY KEY CHECK (method_id ~ '^am_[0-9a-f]{32}$'),
    account_id        TEXT        NOT NULL REFERENCES identity_accounts (account_id),
    kind              TEXT        NOT NULL CHECK (kind IN ('google', 'passkey')),
    key_id            TEXT        NOT NULL CHECK (key_id ~ '^[a-z0-9][a-z0-9_-]{0,31}$'),
    lookup_pseudonym  TEXT        NOT NULL CHECK (lookup_pseudonym ~ '^[0-9a-f]{64}$'),
    created_at        TIMESTAMPTZ NOT NULL,
    last_used_at      TIMESTAMPTZ NOT NULL,
    revoked_at        TIMESTAMPTZ
);

CREATE UNIQUE INDEX identity_auth_methods_lookup
    ON identity_auth_methods (kind, lookup_pseudonym) WHERE revoked_at IS NULL;

CREATE INDEX identity_auth_methods_account ON identity_auth_methods (account_id);

CREATE TABLE identity_account_creation_budget (
    hour_start  TIMESTAMPTZ PRIMARY KEY,
    created     INTEGER     NOT NULL CHECK (created >= 0)
);

CREATE TABLE identity_google_token_replay (
    token_hash  TEXT        PRIMARY KEY CHECK (token_hash ~ '^[0-9a-f]{64}$'),
    expires_at  TIMESTAMPTZ NOT NULL
);

CREATE INDEX identity_google_token_replay_expiry ON identity_google_token_replay (expires_at);
