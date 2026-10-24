-- 0006: the Play Integrity tier a session records (ADR-0031 D31.4).
--
-- A session's tier is 'certified' (its latest verdict passed every check),
-- 'restricted' (a verdict failed a check: ADR-0010 §5(b)'s downgrade) or
-- 'unverified' (no verdict: no token was presented, or no verifier could
-- answer). Every session that exists before this migration was never
-- assessed, so the default is the truth for all of them.
--
-- RECORDED, NOT ENFORCED (ADR-0031 D31.5): nothing authorises on this column
-- until the owner sets what the restricted tier may do.
--
-- FORWARD ONLY, like 0001-0005.
ALTER TABLE identity_sessions
    ADD COLUMN integrity_tier TEXT NOT NULL DEFAULT 'unverified'
        CONSTRAINT identity_sessions_integrity_tier_known
        CHECK (integrity_tier IN ('certified', 'restricted', 'unverified'));
