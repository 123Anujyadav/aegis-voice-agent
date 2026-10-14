-- 0001: carrier call-ID bindings (Phase 19 P1-10).
--
-- One row maps one carrier's call to one platform call. Identifiers only: no
-- phone number, name or call content (frozen invariant I7).
--
-- The constraints ARE the binding contract, enforced by the database so that it
-- holds across every replica, not only inside one process:
--
--   * PRIMARY KEY (provider, provider_call_id): a carrier call ID is unique only
--     within its carrier, so the provider is part of the key.
--   * UNIQUE (call_id): a platform call is bound to at most one carrier call.
--   * CHECKs: the same formats the Go side validates (callback.Binding.Validate),
--     repeated here so a writer that bypassed the Go validation is refused.
--
-- No retention is encoded. ADR-0009 does not specify one for bindings, and none
-- is invented here: rows are removed only by an explicit delete.

CREATE TABLE IF NOT EXISTS telephony_carrier_call_bindings (
    provider          TEXT        NOT NULL,
    provider_call_id  TEXT        NOT NULL,
    call_id           TEXT        NOT NULL,
    bound_at          TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (provider, provider_call_id),

    CONSTRAINT telephony_carrier_call_bindings_call_id_key
        UNIQUE (call_id),

    CONSTRAINT telephony_carrier_call_bindings_provider_format
        CHECK (provider ~ '^[a-z0-9_-]{1,64}$'),

    CONSTRAINT telephony_carrier_call_bindings_provider_call_id_format
        CHECK (provider_call_id ~ '^[A-Za-z0-9._:-]{1,128}$'),

    CONSTRAINT telephony_carrier_call_bindings_call_id_format
        CHECK (call_id ~ '^call_.+$' AND char_length(call_id) <= 128)
);
