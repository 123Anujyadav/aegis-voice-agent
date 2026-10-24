# ADR-0030 decision brief

- **For:** the repository owner.
- **Asks you to:** accept, amend or reject
  [ADR-0030](../adr/0030-identity-hmac-key-separation-and-rotation.md) D30.3,
  together with the two amendments the migration-design validation found.
- **Status:**
  - ADR-0030 is *Proposed: decided under owner delegation, owner ratification
    pending*.
  - D30.3 is marked as the **owner's decision**, because it changes how
    subscriber ids are produced, and those ids are product data.
  - This brief does not make that decision.
- **Detail:** [ADR-0030-D30.3-MIGRATION-PLAN.md](ADR-0030-D30.3-MIGRATION-PLAN.md).
  It describes design validation only: nothing has been migrated, provisioned
  or wired.

## 1. The decisions

| # | Decision | Recommendation | Why |
|---|---|---|---|
| 1 | **D30.3:** stored random subscriber ids, with every existing subscriber's legacy id kept | **Accept** | Today a subscriber id is derived from the single HMAC key, so that key can never be rotated, even after a compromise. Storing ids separates "who is this subscriber" from key hygiene. |
| 2 | **Amendment A: an `id_origin` column** (`legacy` or `random`) in `identity_subscribers` | **Accept** | Rolling back to stage 0 is safe only while every row holds a legacy id. The number is never stored, so a row's origin cannot be worked out later; without the column the rollback guard cannot be checked. Enforced by `CheckStageTransition`, and mutation-checked. |
| 3 | **Amendment B: subscriber keys stay in the lookup ring while any row uses them.** New pseudonyms use only the active key. | **Accept** | D30.2's rule, that the previous key can be dropped after a window, is unsafe for permanent rows. Dropping a key strands dormant subscribers, and when one returns, the UNIQUE id refuses them: **they are locked out** (`TestMigration_DroppingAnOldSubscriberKeyStrandsRows`). Keeping the key prevents this: `TestMigration_KeyringLookupKeepsDormantSubscribersAcrossRotations` shows the id is kept across two rotations, and concurrent returns agree on one id. |
| 4 | **Stage 4:** retiring the legacy key | **Not now.** A separate, later decision. | It is irreversible, and dormant subscribers who return afterwards get new ids. |

## 2. What is irreversible, and the limits of rollback

| Step | Can it be reversed? | Limit |
|---|---|---|
| Stage 0 → 1: read the mapping if present | Yes | — |
| Stage 1 → 2: write legacy-preserving rows | Yes, to stage 1. To stage 0 only while every row is `legacy`. | — |
| Stage 2 → 3: random ids for new numbers | Yes, to stage 2, while the legacy key exists. **Never to stage 0**, because a random id would be silently replaced. | **From here on, backups are a correctness requirement:** a lost random id cannot be recovered. SRE must confirm point-in-time recovery first. |
| Stage 3 → 4: destroy the legacy key | **No.** | Dormant subscribers get new ids. This needs your decision, and `DormantSubscribers` must be empty. |
| Dropping a subscriber key from the lookup ring | **No**, for any row still under that key | It is allowed only when no row uses the key. Rows record `key_id`, as D30.5's table already proposes, so this can be counted. |

## 3. Privacy

- The mapping stores a keyed pseudonym, an opaque id and the origin.
- It holds no number, and nothing that reveals whether a number is
  registered before it is verified.
- A retained old subscriber key lets someone who also has read access to the
  table test whether a given number is registered. It reveals no number.
- If an old key is compromised, it stops being used for new pseudonyms
  (already true for any non-active key) and is kept for lookups only.

## 4. What accepting does and does not authorise

**It authorises** the implementation work in the plan's §7:
- the forward migration;
- the PostgreSQL directory, with real-PostgreSQL tests;
- wiring at stage 0, by configuration.

**It does not authorise:**
- moving any environment past stage 0;
- key provisioning;
- stage 4.

Each stage change is an operational step with its own preconditions,
recorded in the change log.

## 5. If you reject D30.3

- D30.1 and D30.2 (one key per purpose, keyrings) still apply to the OTP,
  refresh and pseudonym keys.
- The subscriber-id key then **can never be rotated**, and a compromise of it
  has no recovery. That is ADR-0030's revisit trigger, and the risk you would
  be accepting.
