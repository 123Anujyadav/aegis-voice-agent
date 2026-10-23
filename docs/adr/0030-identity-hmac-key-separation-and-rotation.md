# ADR-0030: Identity HMAC keys: separation by purpose, and rotation

- **Status:** **Proposed: decided under owner delegation (2026-10-23), owner
  ratification pending.** Claude made the decisions below under the owner's
  standing delegation for in-scope engineering decisions. They are **not** an
  owner-accepted ADR.
  - **D30.3 needs the owner:** it changes how subscriber ids are produced, and
    that is product data.
  - **Implementation waits** for ratification of D30.3. Nothing in this ADR has
    been implemented.
- **Date:** 2026-10-23
- **Deciders:** Claude, under the repository owner's standing delegation;
  ratification by the repository owner
- **Informed:** Security; SRE (secret provisioning)
- **Depends on:** ADR-0010, ADR-0012, ADR-0029

---

## Context

identity has **one** HMAC secret (`CS_IDENTITY_HMAC_SECRET_KEY_FILE`). On
`main` it keys four different things:

| Use | Function | Domain-separated | Lifetime of what it protects |
|---|---|---|---|
| OTP code hash | `hashString(code)` | **no** | 5 minutes |
| Refresh-token hash | `HashRefreshToken` | yes (`…refresh-token.v1`) | the session: 24 hours today |
| MSISDN pseudonym (rate-limit and lockout keys) | `MSISDNPseudonym` | yes (`…msisdn.v1`) | the rate and lockout windows: minutes |
| **Subscriber id** (`usr_` + HMAC of the number) | `subscriberID` | **no** | **forever**: it is the token `sub` |

**What this means:**
- **The key cannot be rotated.** Rotating it would:
  - invalidate every session (the refresh-token hashes);
  - void pending OTPs;
  - **give every subscriber a new id**, orphaning everything keyed by the old
    one.
- **A suspected compromise has no clean recovery.**
- **The two undomained uses share one input space:** a code and a number are
  both HMAC'd under the same key with no separator. A collision is not
  practical, but it is not ruled out by construction either.

The rotation runbook (#107) and ADR-0029's open items record this gap.

**Why now:** identity is pre-production. No real user exists, and outside
identity, subscriber ids appear only in tests. Changing how they are produced
costs nothing today. After launch it needs a data migration.

## Decisions

### D30.1: one key per purpose

**Decided:** four independent keys, each from its own file and each
domain-separated:
- `CS_IDENTITY_OTP_HASH_KEY_FILE`;
- `CS_IDENTITY_REFRESH_HASH_KEY_FILE`;
- `CS_IDENTITY_PSEUDONYM_KEY_FILE`;
- `CS_IDENTITY_SUBSCRIBER_KEY_FILE` (see D30.3).

**Validation:** startup refuses any two that are equal, or equal to the signing
key, as identity already refuses an HMAC key equal to the signing key.

**Why:** a compromise of one purpose's key does not expose the others, and each
can follow the rotation its own lifetime allows.

**Rejected: one key with a key-derivation function per purpose.** It is simpler
to provision, but it couples every rotation together again.

### D30.2: keyrings, with a key id stored beside each hash

**Decided:**
- **Shape:** the OTP, refresh and pseudonym keys are each a keyring (an active
  key and at most one previous key), as edge-api's signing-key set is
  (ADR-0029).
- **Hashes:**
  - every stored hash records the key id it was made with;
  - verification uses that id's key;
  - new hashes always use the active key.
- **Per purpose:**
  - **OTP:** the previous key may be removed 5 minutes after the switch.
  - **Refresh:** refresh tokens rotate on every use, so each session re-hashes
    under the active key at its next refresh. The previous key may be removed
    once the session lifetime (24 hours) has passed since the switch.
  - **Pseudonym:** the raw number is never stored, so pseudonyms cannot be
    recomputed in bulk. They migrate **lazily**: on each OTP request, identity
    computes the pseudonym under both keys and, if it finds state under the
    previous one, moves it to the active one. Rate-limit and lockout windows are
    minutes long, so the previous key may be removed after the longest window
    (the 15-minute lockout) has passed since the switch.
- **Schema:** each affected table gains a key-id column, through a forward
  migration in the style of `0004_msisdn_pseudonyms`.

### D30.3: subscriber ids are stored random values, not derived (OWNER DECISION)

**Proposed, and needing the owner:**
- **Change:** a subscriber id becomes a random `usr_` value, created once at the
  number's first successful enrolment.
- **Storage:** it is stored in a new table keyed by the subscriber pseudonym,
  `identity_subscribers(pseudonym, subscriber_id)`.
- **Its key:** `CS_IDENTITY_SUBSCRIBER_KEY_FILE` keys only that lookup. It is
  rotated lazily, as in D30.2, and **rotating it never changes a subscriber
  id**.

**Why:** an id derived from a key can never outlive the key. Storing the id
separates identity ("who is this subscriber") from key hygiene.

**Consequence:** subscriber ids become state that must be backed up with the
database. A lost table means new ids, exactly as a lost key does today.

**Alternatives:**
- **Keep the derived id, and never rotate its key.** This is a smaller change,
  but a compromise of that key still has no recovery.
- **Store the id, encrypted under a separate key.** This adds no value over a
  random id plus a keyed lookup.

### D30.4: migration and order

1. **Schema:** add the key-id columns and `identity_subscribers` (forward only).
2. **Keys:** introduce the per-purpose keys. The existing key becomes the
   **previous** key of every ring, so nothing in flight breaks.
3. **Subscriber ids:** backfill `identity_subscribers` lazily at each
   subscriber's next enrolment. Pre-production there is nothing to backfill.
4. **Retire the single key** once every ring's previous-key window has passed.

## Consequences

- **Positive:**
  - Each purpose's key is rotatable, with a defined overlap window.
  - A compromise of one purpose is contained.
  - The rotation runbook gains a real HMAC procedure.
- **Negative:**
  - There are four secrets instead of one.
  - A key-id column is added to each affected table.
  - The OTP request path computes a pseudonym twice during a pseudonym-key
    overlap.
- **Not changed:** access-token signing (ADR-0029), DPoP, and every HTTP
  contract.

## Revisit triggers

- **The owner rejects D30.3.** Then D30.1–D30.2 still apply, and the subscriber
  key is documented as unrotatable.
- **Session lifetime changes** (ADR-0029's 24-hour deviation). The refresh
  ring's overlap window follows it.
- **identity reaches production before this is implemented.** The migration in
  D30.4 then needs a real backfill plan.
