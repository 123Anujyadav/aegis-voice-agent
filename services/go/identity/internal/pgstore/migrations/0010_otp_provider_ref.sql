-- 0010: challenges whose code a provider generated (an app.OtpVerifier, such
-- as Twilio Verify; docs/phase19/OTP-PROVIDERS.md).
--
-- identity_otp_challenges.provider_ref: the provider's verification id, which
-- is not personal data. Such a challenge has no code_hash: identity never sees
-- its code. Every other challenge keeps its code_hash and has no provider_ref.
-- The CHECK makes that exactly-one rule hold in the database, so a challenge
-- with neither (which no code could match) or both cannot be stored.
--
-- FORWARD ONLY, like 0001-0009. The new column is NULL for every existing
-- row, and every existing row has a non-empty code_hash, so the CHECK holds
-- for them; it changes no data.

ALTER TABLE identity_otp_challenges
    ADD COLUMN provider_ref TEXT
        CHECK (provider_ref ~ '^[A-Za-z0-9]{2,64}$');

ALTER TABLE identity_otp_challenges
    ADD CONSTRAINT identity_otp_challenges_one_code_source
        CHECK ((provider_ref IS NULL AND code_hash <> '') OR (provider_ref IS NOT NULL AND code_hash = ''));
