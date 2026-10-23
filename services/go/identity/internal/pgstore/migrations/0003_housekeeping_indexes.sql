-- 0003: indexes for bounded housekeeping.
--
-- Housekeeping deletes by age, in batches. The existing (msisdn, time) indexes
-- on these two tables lead with msisdn and cannot serve "oldest rows first"
-- across all numbers; without these, every batch would scan the table.
--
-- The other tables housekeeping touches already have what it needs:
-- identity_otp_challenges (expires_at), identity_sessions (expires_at) and
-- identity_dpop_replay (expires_at) from 0001.
--
-- Plain CREATE INDEX, inside the migration's transaction: it blocks writes to
-- these two tables while it builds. They are small, short-lived rate-limit
-- tables; CREATE INDEX CONCURRENTLY cannot run inside a transaction, and the
-- migration runner's one-transaction-per-step guarantee matters more.
--
-- FORWARD ONLY, like 0001 and 0002.

CREATE INDEX identity_otp_requests_requested_at ON identity_otp_requests (requested_at);
CREATE INDEX identity_otp_failures_failed_at ON identity_otp_failures (failed_at);
