-- 0010: the global hourly sign-in delivery cap (F-1; ADR-0033 §7, accepted
-- for M1 on 2026-11-12; internal/app/signin_controls.go).
--
-- identity_signin_delivery_budget: the count of sign-in codes handed to the
-- SMS sender in each clock hour (UTC), so the deployment's cap holds across
-- replicas. One row per hour, holding a count and nothing else: no number, no
-- pseudonym, no code. The repository increments it only while it is below the
-- cap, in one statement (pgstore/signin_budget.go). Housekeeping deletes old
-- hours, like identity_deletion_code_budget (0009).
--
-- FORWARD ONLY, like 0001-0009. It creates one new table and touches no
-- existing table or row.

CREATE TABLE identity_signin_delivery_budget (
    hour_start  TIMESTAMPTZ PRIMARY KEY,
    deliveries  INTEGER     NOT NULL CHECK (deliveries >= 0)
);
