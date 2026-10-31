# ADR-0032 architecture review packet

- **For:** the repository owner, who holds final architecture authority
  (`ARCHITECTURE_FREEZE.md` §6.1, adopted in #199), and the independent human
  reviewer that rule 8 requires, once one is identified. Also Security,
  privacy and legal, and SRE.
- **Reviews:**
  [ADR-0032: Additional authentication methods](../0032-additional-authentication-methods.md)
  - Status: **Proposed: high-risk; awaiting independent human review. Not
    accepted, not ratified, not implemented.**
  - Version: the file as published with this packet (1006 lines,
    SHA-256 `ad8ff9e8…76026a40c`). It is the version analysed on
    2026-10-31 (957 lines, `30e88ed6…f56e4bf64`) plus status and governance
    edits for P2 and Markdown spacing; no decision content changed.
  - Repository base: `main` `cef3695` (#199).
- **Prepared by:** Claude Code. **This packet is an AI-performed analysis,
  not an independent human review** (§6.1, rule 9). It cannot satisfy rule 8.
- **What this packet is:** the material for a decision. **It decides
  nothing.** It does not accept ADR-0032, change ADR-0010 or any accepted
  ADR, change an invariant, or answer an owner decision.
- **Date:** 2026-10-31

**How to read it:** §1–§2 give the direction and the accepted constraints.
§3 holds the nine architecture-team decisions as cards. §4–§10 go deeper on
the topics those cards depend on, adding four invariant sign-off cards and
three decisions shared with the owner and privacy review. §11 is the risk
register. §12 is the ordered list of approvals that gate implementation.

**Every decision card has the same fields:** current state, why the decision
is needed, options, security and privacy consequences, dependencies, evidence
recommended for approval, and approvers. Where ADR-0032 proposes an option,
the card says "ADR-0032 proposes"; that is a proposal, not an outcome.

**Approver roles used below:**

| Role | Where its authority comes from |
|---|---|
| **Architecture team** | Under `ARCHITECTURE_FREEZE.md` §6.1 (#199): the repository owner's recorded decision, plus an independent human review for every high-risk decision in this packet. Rules 2, 3 and 5 (`:142-150`) |
| **Security** | CODEOWNERS `/services/go/identity/` (teams: backend, security) |
| **Privacy and legal** | CODEOWNERS `/docs/compliance/` (teams: compliance, security, legal); ADR-0012 |
| **SRE** | CODEOWNERS `/docs/runbooks/` (teams: sre); ADR-0008 |
| **Owner** | The repository owner: product decisions and the owner-reserved items |

> **Governance (resolved by #199, 2026-10-31):** every CODEOWNERS path names
> the same single account, and no architecture team exists. The owner adopted
> governance option P2 (`ARCHITECTURE_FREEZE.md` §6.1; `docs/adr/README.md`,
> "Approval"): the owner decides, and every high-risk decision also needs an
> independent human review. **No independent human reviewer has been
> identified, so every high-risk decision below stays Proposed** (rule 10).

---

## 1. Executive summary and proposed direction

**The owner's product direction (2026-10-31):**
1. **Primary:** passwordless email one-time code (6 digits).
2. **Alternative:** Sign in with Google.
3. **SMS one-time codes:** deferred, not cancelled. They return as **phone-
   ownership verification**, not as sign-in.

**What ADR-0032 proposes** (ADR-0032 §"Decision Outcome", `:197-736`):
- **The account becomes a stored random subscriber id** (D32.1). Email,
  Google and a later verified number are *external identifiers* attached to
  it, each stored only as a keyed pseudonym.
- **Both new methods issue exactly today's session** (D32.6): a DPoP proof at
  issuance, a `cnf.jkt`-bound EdDSA access token (ADR-0029), a rotating
  refresh token with reuse detection, and the optional Play Integrity tier
  (ADR-0031). No bearer session from any method.
- **Phone ownership is a separate, server-enforced capability** (D32.5). No
  email code, Google token or client-supplied number ever verifies a number.
- **Linking is never by email address** (D32.4). Only from an authenticated
  session plus a fresh proof of the second method. No merges.
- **Email delivery** through a provider-neutral sender with the `otpdelivery`
  semantics. Amazon SES in `ap-south-1` through the SES API and IRSA is the
  candidate consistent with ADR-0008 (D32.8). None is chosen.

**Why this needs the architecture team:** it replaces the identity primitive
of an **Accepted** ADR (ADR-0010 §5(a)), and touches four identity invariants,
one of which (INV-ID-8) carries frozen invariant I2.

**What it does not change:** the device-bound session, the token profile,
refresh rotation, edge-api's local verification, in-app deletion, and INV-ID-3,
INV-ID-9 and INV-ID-10 (no knowledge factor: neither method is a password).

**Readiness:** ADR-0032 is ready for review. It is **not** ready to accept:
16 decisions below are open, and none has a recorded approval.

---

## 2. ADR-0010 constraints that require review

ADR-0010 is **Accepted** (2026-08-06) and **is not modified** by ADR-0032 or
this packet. Under `ARCHITECTURE_FREEZE.md` rule 1 it can only be superseded.

| # | ADR-0010 text | Where | Conflict with the direction | Card |
|---|---|---|---|---|
| C1 | "The subscriber's identity in this product **is their phone number**" | `:14` | The account would be a random id; the number becomes an optional, verified attribute | A2 |
| C2 | §5(a): "Who owns the number — MSISDN verification. SMS OTP at enrolment and on re-verification" | `:65-69` | Enrolment would be by email code or Google; SMS moves to a later verification step | A2, A9 |
| C3 | Option 2 (hosted IdP) rejected for **fit**: "our identity primitive is an MSISDN … not an email/password account" | `:161-167` | The rejection's premise changes. ADR-0032 still rejects a hosted IdP, on residency and the device-bound session | A2 |
| C4 | §9: token verification is local in edge-api, so identity is not a synchronous dependency | `:183-187`, `:304` | Option (b) for phone-ownership state is a lookup at identity | A5 |
| C5 | §10: SIM swap controls (cooling-off, notice to the old device, re-verification of elevated-risk operations) | `:207-217` | They are written for SIM swap; their analogue for a compromised mailbox or Google account is undefined | A9, R1 |
| C6 | §16 review triggers, including "Expansion to a market where SMS OTP is not viable or where a different identity primitive is conventional" | `:308-320` | **No trigger has been shown to have fired.** The direction is a product decision | A1 |

**What still holds, and ADR-0032 keeps:** §5(b) Play Integrity at issuance
and refresh; §5(c) the hardware-bound key (as amended by ADR-0029); the
rejection of bearer tokens (`:108-112`).

---

## 3. The nine architecture-team decisions

These are ADR-0032's architecture decisions (`:856-889`), in its order.

### A1. Is the owner's product direction a sufficient review trigger for ADR-0010?

- **Current state:** ADR-0010 is Accepted. Freeze rule 2 (`:142-143`): "If it
  has not fired, the decision stays. 'I would have chosen differently' is not
  a trigger." No §16 trigger has been shown to fire (C6).
- **Why needed:** every other decision in this packet presumes ADR-0010 can
  be superseded. If it cannot, ADR-0032 cannot be accepted.
- **Options:**
  - **(a)** Rule that the direction meets an existing trigger. The nearest is
    "SMS OTP is not viable": SMS sign-in is blocked on a provider and India
    DLT registration (E2). The team must judge whether "not available yet"
    is "not viable".
  - **(b)** Rule that it does not, and record the concern **as an RFC**, as
    the freeze's §6 flow prescribes (`ARCHITECTURE_FREEZE.md:119-137`).
    ADR-0032 stays Proposed.
  - **(c)** Add or amend a trigger through its own superseding ADR first.
    Note that this is itself a superseding change and needs the same gate.
- **Security and privacy consequences:** none directly. Accepting a weak
  trigger sets a precedent that frozen decisions yield to preference, which
  the freeze exists to prevent.
- **Dependencies:** none. **This is the first gate.**
- **Evidence for approval:** a written trigger assessment naming the trigger
  and the observable fact that fired it (for example the SMS-provider and DLT
  status in `docs/phase19/EXTERNAL-DEPENDENCIES.md`); the owner's written
  direction.
- **Approvers:** Architecture team. The owner supplies the direction but
  cannot make the trigger ruling alone under rule 2.

### A2. A superseding decision for ADR-0010 §5(a), and the freeze summary row

- **Current state:** ADR-0010 §5(a) makes the MSISDN the identity primitive;
  `ARCHITECTURE_FREEZE.md:115` summarises "Auth | MSISDN + OTP · Keystore PoP
  · Play Integrity · EdDSA JWT".
- **Why needed:** without supersession, email and Google sign-in contradict
  an Accepted ADR (C1–C3). Rule 3: a superseding ADR needs architecture-team
  approval.
- **Options:**
  - **(a)** ADR-0032, once approved, supersedes **only §5(a)** and the
    "fit" ground of Option 2. ADR-0010 is marked "§5(a) superseded by
    ADR-0032"; §5(b), §5(c), §9 and §10 stand.
  - **(b)** A wider replacement of ADR-0010. Not proposed: the rest of
    ADR-0010 is not in conflict.
  - **(c)** Reject. Option 1 of ADR-0032 (SMS only) stands.
- **Security and privacy consequences:**
  - The number stops being proven at sign-up, so **every** number-dependent
    feature needs its own gate (A5, §8).
  - New personal data: the email address and the Google identity.
  - The account survives a number change, and SIM swap stops being a path to
    account takeover. A compromised mailbox or Google account becomes one.
- **Dependencies:** A1. A3 (invariants) must be signed off with it, not
  after it (freeze rule 5).
- **Evidence for approval:** the final ADR-0032 text; a diagram update in the
  same pull request (freeze rule 4); the threat comparison of SIM swap versus
  mailbox and Google compromise (§11, R1).
- **Approvers:** Architecture team; Security; the owner (ratification).

### A3. Explicit sign-off of the affected identity invariants

- **Current state:** `docs/domain/10-identity.md:441-450`. Freeze rule 5
  (`:148-150`): invariants need explicit sign-off, "not merely a superseding
  ADR". `DOMAIN_FREEZE.md` rule 1 (`:207-208`, document still PROPOSED): a
  domain invariant traced to I1–I12 needs the same sign-off as the frozen
  invariant.
- **Why needed:** the four invariants in §4 would change in meaning or scope.
- **Options:** per invariant, in §4 (S1–S4).
- **Security and privacy consequences:** per invariant, in §4.
- **Dependencies:** A2 (same pull request); X2 for INV-ID-7; privacy and
  legal for INV-ID-7 and INV-ID-8.
- **Evidence for approval:** for each, the proposed new wording, the
  enforcement point in code, and a test that would fail if it were broken.
- **Approvers:** Architecture team; for INV-ID-8 (I2), the same sign-off as
  the frozen invariant; for INV-ID-7, privacy and legal as well.

### A4. The Google ID-token verifier library, as its own ADR

- **Current state:** ADR-0029 decides a hand-written, single-algorithm token
  parser for first-party tokens and **no remote JWKS** (`:129-132`). Its
  revisit trigger: if the profile widens ("more algorithms … remote JWKS
  fetching"), "adopt a maintained library through its own ADR" (`:106-108`).
  Google ID tokens are RS256, verified against Google's published, rotating
  keys.
- **Why needed:** Google sign-in cannot be verified without a remote key set
  and a second algorithm. ADR-0029's own rule sends that to a separate ADR.
- **Options:**
  - **(a)** A maintained, general JOSE/JWT library, configured for RS256 and
    Google's key set only.
  - **(b)** Google's own Go library for ID-token validation.
  - **(c)** Extend the hand-written parser. **Excluded** by ADR-0029
    `:106-108`.
- **Security and privacy consequences:**
  - A new dependency inside the authentication path: algorithm confusion,
    `kid` handling and key caching must be correct.
  - **Key-fetch failure must fail closed** (ADR-0032 D32.3).
  - First-party tokens must keep using ADR-0029's parser; the library must
    not become a second path that accepts our own tokens.
- **Dependencies:** none to start; it gates Google sign-in only, not email.
- **Evidence for approval:** a comparison of candidates (maintenance,
  vulnerability history, licence, algorithm allow-listing, key-cache
  behaviour, fail-closed behaviour); a test plan covering Google's documented
  checks (signature, `aud`, both `iss` spellings, `exp`); fuzz tests in the
  style of ADR-0029 §(c).
- **Approvers:** Architecture team; Security.

### A5. How edge-api learns whether the account has a verified number

- **Current state:** no number-dependent endpoint exists; edge-api serves
  only `GET /v1/me`. ADR-0029 fixes the claim set (`:49`: "Claims are exactly
  iss, aud, sub, iat, nbf, exp, jti, `cnf.jkt` and roles") and leaves role
  vocabulary to product policy (`:140-141`).
- **Why needed:** the server-side phone gate (§8) needs an input on every
  number-dependent request.
- **Options:**
  - **(a1)** A new token claim. **Changes ADR-0029's exact claim set**, so
    ADR-0029 must be amended (it is Proposed, owner ratification pending).
  - **(a2)** A value in the existing `roles` claim. No claim-set change, but
    authorisation state in a self-contained token.
  - **(b)** A lookup at identity per request. Always current, but against
    ADR-0010 §9's local verification (C4).
  - **(c)** (a1) or (a2) for routine checks, plus (b) for ADR-0010 §10
    elevated-risk operations only.
- **Security and privacy consequences:**
  - (a1) and (a2): the token can lag a removal by up to **15 minutes**
    (ADR-0032 P7; the same residual window as D9/O17).
  - (b): identity's availability becomes the availability of every
    number-dependent feature.
  - All options: the token never carries the number itself.
- **Dependencies:** A2; ADR-0029 ratification if (a1); owner decision 9
  (shared with the owner).
- **Evidence for approval:** the list of planned number-dependent endpoints
  and their staleness tolerance; a latency and availability estimate for (b);
  a test showing a removed number is refused within the stated window.
- **Approvers:** Architecture team; Security; the owner (decision 9).

### A6. The domain and UX specifications

- **Current state:** `docs/domain/10-identity.md` anchors `Subscriber` on the
  MSISDN (`:18`), creates it after a `VerificationChallenge`, and validates
  "Exactly one active MSISDN" (`:72-75`). UX screens A02 (phone number), A03
  (code) and A15/A16 (re-verification, device move) assume SMS.
- **Why needed:** freeze rule 4 and `DOMAIN_FREEZE.md` rule 2 require the
  language and diagrams to change with the decision, everywhere at once.
- **Options:** update in the same pull request as A2 (recommended by the
  freeze rules), or in a follow-up gated before implementation.
- **Security and privacy consequences:** an out-of-date specification is how
  an implementer reintroduces "number = account" (§8, P4).
- **Dependencies:** A2, A3, X3 (onboarding order).
- **Evidence for approval:** the diff of `10-identity.md` (new identifier
  entity, lifecycle, validation rules) and of the onboarding screens.
- **Approvers:** Architecture team; the UX specification's owner (the owner).

### A7. ADR-0030 amendments

Detail in §5.1.
- **Current state:** ADR-0030 is Proposed (owner ratification pending); its
  D30.3 (random subscriber ids) is an owner decision (O2). D30.3 and the
  migration plan key the id table by the **number's** pseudonym, with
  `subscriber_id UNIQUE` (`docs/phase19/ADR-0030-D30.3-MIGRATION-PLAN.md:112-125`).
- **Why needed:** ADR-0032 needs several identifiers per subscriber. Under
  `subscriber_id UNIQUE`, a subscriber can have only one row.
- **Options:**
  - **(a)** Generalise: `identity_subscribers` keeps one row per subscriber
    (id, origin, created), and a new identifier table maps
    `(kind, key id, pseudonym) → subscriber id`, unique per
    `(kind, pseudonym)`.
  - **(b)** Widen the existing table with a `kind` column and drop
    `subscriber_id UNIQUE`. Simpler, but mixes the account and its
    identifiers.
  - **(c)** Keep D30.3 number-only and add a parallel table for email and
    Google. Not recommended: two sources of truth for "which account".
- **Security and privacy consequences:** the uniqueness constraint *is* the
  "no merge, no takeover by identifier" rule (D32.4). A wrong key retirement
  locks dormant holders out (the brief's Amendment B).
- **Dependencies:** O2 ratification; A2.
- **Evidence for approval:** a revised migration plan with the multi-kind
  schema; tests in the style of `subscriber_migration_test.go`
  (`TestMigration_KeyringLookupKeepsDormantSubscribersAcrossRotations`) for
  each identifier kind; the key-purpose decision (§5.1).
- **Approvers:** the owner (O2, product data); Architecture team; Security.

### A8. The SES integration's security shape

Detail in §7.
- **Current state:** no email sender exists. identity has no AWS SDK; the
  telephony gateway already uses `aws-sdk-go-v2`. ADR-0008: "IRSA for every
  workload. No static cloud credentials anywhere" (`:73`, `:180`).
- **Why needed:** the first draft of ADR-0032 claimed "no static credential"
  without a mechanism; the review corrected it. The mechanism, the role's
  scope and the dependency need approval.
- **Options:** SES API v2 with SigV4 from IRSA (proposed); the SES SMTP
  interface (rejected: needs a long-lived IAM-user key); a third-party
  provider with an API key (needs a ruling on ADR-0008's scope).
- **Security and privacy consequences:** see §7.
- **Dependencies:** owner decision 7 (provider, region, domain); SRE; privacy
  review (residency, DPA).
- **Evidence for approval:** §7.4.
- **Approvers:** Architecture team; Security; SRE; privacy and legal for the
  provider.

### A9. The SMS route's redesign, and "sign out everywhere"

Detail in §8.3 and §11.
- **Current state:**
  - `/v1/otp/validate` (`ValidateOtp`, `internal/app/identity.go:1371`)
    issues a session for an account **derived from the number**. It is
    dormant only because no SMS provider is wired.
  - identity revokes one session at a time (`RevokeSession`) or everything
    at erasure. There is no "sign out every device"; a new sign-in does not
    revoke the account's other sessions.
- **Why needed:**
  - Re-enabled unchanged beside email accounts, the SMS route would create a
    second, number-keyed account that holds the number implicitly: a bypass
    of the phone gate (ADR-0032 P4).
  - The response to a compromised mailbox or Google account needs a way to
    end every session.
- **Options:**
  - SMS route: **(a)** redesign it as "attach a verified number to the
    signed-in account" before SMS returns (proposed); **(b)** keep it as
    sign-in and accept number-keyed accounts beside email ones (contradicts
    D32.5); **(c)** remove it and write a new verification route.
  - Revocation: **(a)** revoke every session family of the account, from any
    signed-in device and after a suspicious enrolment; **(b)** revoke the
    sessions created by one sign-in method; **(c)** none (status quo).
- **Security and privacy consequences:** (b) for SMS re-opens the bypass.
  Without revocation, the holder of a recovered account cannot evict an
  attacker; access tokens still live up to 15 minutes either way (D9/O17).
- **Dependencies:** A5; owner decisions 4 and 11; the SIM-swap review's open
  items (`docs/phase19/SIM-SWAP-CONTROLS-REVIEW.md` §2–§4).
- **Evidence for approval:** a guard test that the SMS sign-in route cannot
  be enabled while email or Google sign-in is enabled; the revocation design
  with its audit records.
- **Approvers:** Architecture team; Security; the owner (decision 11).

---

## 4. Identity invariants requiring sign-off

`docs/domain/10-identity.md:441-450`. Nothing here changes them; each card
states what ADR-0032 would need.

### S1. INV-ID-1: "A Subscriber has exactly one active MSISDN" (source: ADR-0010)

- **Current state:** enforced structurally: the subscriber id is derived from
  the number, so an account without one cannot exist.
- **Why needed:** email and Google accounts have no number until it is
  verified.
- **Options:** **(a)** "at most one **verified** MSISDN" (ADR-0032 proposes);
  **(b)** keep "exactly one" and require SMS at sign-up (Option 1).
- **Security and privacy consequences:** under (a), "has a number" can no
  longer be assumed anywhere; every consumer must check (§8).
- **Dependencies:** A2, A5, A7.
- **Evidence for approval:** an inventory of code and documents that assume
  a subscriber has a number (the telephony association, forwarding, DID
  routing) and how each will check.
- **Approvers:** Architecture team; the owner.

### S2. INV-ID-2: "An MSISDN maps to at most one active Subscriber" (source: ADR-0010)

- **Current state:** enforced structurally by derivation.
- **Why needed:** a number now attaches to a random account, so the
  uniqueness must become a constraint.
- **Options:** **(a)** scope it to verified numbers and enforce it when a
  number is verified (ADR-0032 proposes; refuse a number verified elsewhere,
  owner decision 10); **(b)** allow a support-led transfer.
- **Security and privacy consequences:** a duplicate account (email and
  Google not linked) can verify a number on only one of them; a recycled
  number held by a previous account must be handled without disclosing that
  account.
- **Dependencies:** A7 (the uniqueness constraint), A9, owner decision 10.
- **Evidence for approval:** the constraint and a concurrency test (two
  accounts verifying one number at once: exactly one succeeds).
- **Approvers:** Architecture team; Security; the owner.

### S3. INV-ID-7: "An erased Subscriber's identifiers are tombstoned and never reused" (source: DPDP)

- **Current state:** see §9.
- **Why needed:** tombstoning the new identifiers means retaining their
  pseudonyms; erasing them means they can be reused.
- **Options:** §9 (X2).
- **Security and privacy consequences:** §9.
- **Dependencies:** X2; L2; owner decision 12 (D1/O9).
- **Evidence for approval:** §9.
- **Approvers:** Architecture team; privacy and legal; the owner.

### S4. INV-ID-8: "Every `PERSONAL` attribute is residency-bound …" (source: frozen invariant **I2**)

- **Current state:** I2 (`ARCHITECTURE_FREEZE.md:64`): personal data does not
  leave India except under the four-condition consent gate. ADR-0012 C4:
  "at rest and in normal processing stays in India".
- **Why needed:**
  - The email address and the code go to the person's own mailbox provider,
    which may be outside India.
  - The Google identity is held by Google; we receive the ID token only.
- **Options:**
  - **(a)** Treat delivery to the person's chosen mailbox, and their own
    Google account, as outside I2's scope: the data goes to the data
    principal, not to our processor. **Needs a legal ruling.**
  - **(b)** Treat it as a transfer and put it under the consent gate.
  - **(c)** Neither fits: reject email delivery to non-Indian mailboxes
    (impractical: the mailbox location is unknowable).
- **Security and privacy consequences:** our processor stays in India under
  SES `ap-south-1`; the mailbox and Google are outside our control.
- **Dependencies:** A8 (provider region); L1; the DPA.
- **Evidence for approval:** a legal opinion on (a); the provider's
  processing location and DPA; the notice text (L4).
- **Approvers:** the same sign-off as frozen invariant I2 (architecture team
  under freeze rule 5); privacy and legal.

**Unchanged and recorded for completeness:** INV-ID-3 (device credential
on-device, non-exportable), INV-ID-9 (no `ACTIVE` subscriber without a
`TRUSTED` device), INV-ID-10 (no knowledge factor).

---

## 5. Conflicts with ADR-0030 and ADR-0029

Both are **Proposed: decided under owner delegation, owner ratification
pending.** Amending them is possible, but each amendment needs that
ratification.

### 5.1 ADR-0030

| # | ADR-0030 says | ADR-0032 needs | Card |
|---|---|---|---|
| K1 | D30.3: a random id "created once at the **number's** first successful enrolment", stored in `identity_subscribers(pseudonym, subscriber_id)` (`:92-98`); the migration plan makes `subscriber_id UNIQUE` (`MIGRATION-PLAN.md:112-125`) | Ids created at the first sign-in by **any** method; several identifier rows per subscriber | A7 |
| K2 | D30.1: four keys, one per purpose; the pseudonym key keys rate limits for minutes | Email and Google pseudonyms key an account **for life**: a new purpose. Share the subscriber-lookup key, or a fifth key? | A7 |
| K3 | D30.2: the previous pseudonym key may go after the longest window (15 minutes) | A lifetime lookup key's previous version **cannot be removed on a timer**. The decision brief's Amendment B already says this for subscriber keys (`ADR-0030-DECISION-BRIEF.md:23`) | A7 |
| K4 | D30.4/D30.5: lazy migration at each subscriber's next enrolment, by number | Lazy re-keying still works, since each identifier is presented again at sign-in | none |

**Order:** O2 (D30.3) must be ratified **before** ADR-0032 can be accepted;
ADR-0032 has no id to give a non-number account otherwise.

### 5.2 ADR-0029

| # | ADR-0029 says | ADR-0032 needs | Card |
|---|---|---|---|
| K5 | Claims are exactly iss, aud, sub, iat, nbf, exp, jti, `cnf.jkt`, roles (`:49`) | A phone-ownership signal in the token, if A5 chooses (a1) | A5 |
| K6 | Remote JWKS or more algorithms means a maintained library "through its own ADR" (`:106-108`); no remote JWKS for **first-party** tokens (`:129-132`) | Google tokens: RS256 and Google's remote keys | A4 |
| K7 | The token `sub` is the subscriber id | Unchanged: `sub` stays the **internal** id; no Google or email value enters our token | none |

**No conflict** with ADR-0029's device binding, refresh rotation, lifetimes
or key rotation: ADR-0032 reuses them unchanged.

---

## 6. Email pseudonym versus encrypted address

### X1. Is a recoverable email address kept? (owner decision 5, with privacy review)

- **Current state:** nothing is stored; no email column exists in any
  migration (0001–0009). ADR-0032 D32.2 (`:298-357`) proposes the lookup key
  `HMAC(pseudonym key, "callscreen.identity.email.v1\0" + address)` and
  compares the designs.
- **Why needed:** a one-way pseudonym can find an account but **cannot be
  mailed**. Every message we start ourselves depends on this choice.
- **Options:**

| | **A. Pseudonym only; the user types the address each time** (ADR-0032's proposed default) | **B. Pseudonym plus an address encrypted under a KMS key in `ap-south-1`** | **C. Plaintext column** |
|---|---|---|---|
| Sign-in, sign-up, web deletion by email | Work | Work | Work |
| Mail we start: new-sign-in alerts, deletion confirmation, breach notices | **Impossible** | Possible | Possible |
| "Which address did I use?" | Unanswerable | A masked hint to a proven holder | Answerable |
| Database leak alone | Pseudonyms, useless without the key | Ciphertext, useless without KMS access | **Addresses exposed** |
| Erasure | Delete the row | Delete the row; backups hold ciphertext until expiry (ADR-0012 C5) | Delete the row; backups hold plaintext |
| Data safety (L5) | Collected, not retained | Collected and retained | Collected and retained |

  ADR-0032 rejects C.
- **Security and privacy consequences:**
  - A minimises data but removes email as a warning channel. With no push
    built (`packages/go/integrations` has only an `FcmPushDispatcher`
    interface and a mock), **a holder whose mailbox is compromised cannot be
    warned at all** under A, today.
  - B adds KMS key custody, a decrypt permission, and a second key to rotate.
  - Under both, compromise of the pseudonym key lets an attacker test
    guessed addresses offline.
  - **Breach intimation:** ADR-0012 C8 records DPDP §8(6). Whether in-app
    notice satisfies it when no address is held is for legal (L1).
- **Dependencies:** A7 (key purpose); A8 (KMS under IRSA for B); L1, L5;
  push notifications (owner, `SIM-SWAP-CONTROLS-REVIEW.md` §1).
- **Evidence for approval:** a legal view on breach intimation without an
  address; for B, a KMS key policy draft (decrypt only by identity's role)
  and a test that ciphertext never reaches logs or events.
- **Approvers:** the owner (decision 5); privacy and legal; Security;
  Architecture team for the storage shape.

---

## 7. AWS SES API and IAM authentication design

Part of A8. ADR-0032 D32.8 (`:612-704`).

### 7.1 The mechanism

| Mechanism | What it is | Status in ADR-0032 |
|---|---|---|
| **SES API v2 (`SendEmail`), SigV4, IRSA** | The pod's projected service-account token is exchanged by the AWS SDK with STS (`AssumeRoleWithWebIdentity`) for **temporary** credentials, refreshed automatically | **Proposed** |
| SES SMTP interface | Credentials are an IAM user's access key id plus a password **derived from that user's long-lived secret access key**, per region; AWS states they cannot be derived from temporary credentials | **Rejected** (ADR-0008) |
| IAM-user access key in a Secret or the environment | A long-lived static cloud credential | **Rejected** (ADR-0008) |

**The precise claim:** identity holds **no long-lived AWS credential**. It
is not "credential-free": it uses short-lived STS credentials from its role.

### 7.2 Role and permissions (SRE to define; nothing created)

- `ses:SendEmail` only, conditioned to the verified sending identity and, if
  SRE confirms the condition keys, the `From` address.
- A separate least-privilege permission to read bounce and complaint events
  (an SES event destination, for example SNS to SQS).
- No IAM, identity-management or suppression-list administration rights.
- For design B (X1): `kms:Decrypt` and `kms:Encrypt` on one key, for this
  role only.

### 7.3 Operational facts

- **Sandbox, per region:** verified recipients only, at most 200 messages a
  day and 1 a second, until production access is granted on request. The
  request is an external step; **it has not been made.**
- **Dependency:** identity gains `aws-sdk-go-v2` (SES and the web-identity
  credential provider). The telephony gateway already uses it.
- **Egress:** the regional SES API and STS endpoints. Whether VPC endpoints
  exist for both is for SRE to confirm.
- **Sending domain:** a dedicated subdomain (domain undecided, B3), custom
  MAIL FROM, SPF and DKIM aligned, DMARC from `p=none` tightening to
  `reject`. Gmail's sender thresholds were read from secondary sources and
  must be confirmed against Google's page.
- **Cost:** unverified.

### 7.4 Evidence for approval

- The IAM policy document as a draft for review (not applied).
- A dependency review of the SES client.
- Configuration showing **no** access-key, SMTP-password or secret variable
  for email in identity; a startup test that refuses one if present.
- A fake of the SES API's documented interface for CI; never the live
  service.
- The bounce-handling design: suppression keyed by the address pseudonym,
  and the same answer to a suppressed address as to any other.

### 7.5 Third-party alternative

An API key for a non-AWS provider is a **long-lived third-party secret**:
mounted Secret file, read once, never in the environment or a log, as the SMS
contract requires (`docs/runbooks/identity-provider-onboarding.md` §1a).
**The architecture team must rule whether ADR-0008's rule extends to such
keys.** A residency and DPA assessment is needed either way.

**Approvers:** Architecture team; Security; SRE; privacy and legal (provider);
the owner (provider, region and domain: decision 7).

---

## 8. Phone ownership verification and server-side enforcement

Covers A5 and A9. ADR-0032 D32.5 (`:448-500`).

### 8.1 The rule

- A verified number is attached **only** by an independent ownership proof:
  SMS one-time code (deferred) or silent carrier verification (ADR-0010
  Option 5).
- **Never** by an email code, a Google token, or a number typed, read from
  the SIM, or pre-filled.
- **The gate is on the server.** It applies to forwarding provisioning (C8),
  DID routing, call-list sync (C7), the telephony gateway's association, and
  ADR-0010 §10's elevated-risk operations. None exists yet; the rule binds
  every future one.

### 8.2 Bypass paths and their closing rules (ADR-0032 P1–P8)

| # | Path | Closing rule | Evidence to require |
|---|---|---|---|
| P1 | Any valid access token | Endpoints check the verified-number state, not just a token | A negative test per endpoint: a valid session without a verified number is refused |
| P2 | A client-supplied number | Ignored; only the server's verified record is used | A test sending a different number in the request |
| P3 | A number in a Google token or profile | Not requested, read or stored | Scope review of the Google request |
| P4 | Today's SMS sign-in re-enabled unchanged | Must not be; redesigned as "attach" (A9) | A guard test (A9) |
| P5 | Linking | Attaches email and Google only | A test that linking cannot attach a number |
| P6 | Account recovery | Returns the verified number as it was, without refreshing verification; ADR-0010 §10 analogue after a new-device sign-in | Owner decision 11 |
| P7 | A stale token claim (A5 a1/a2) | Up to 15 minutes; elevated-risk operations recheck at identity | A test of the stated window |
| P8 | Another service trusting `sub` alone | Associations only through identity's verified record | An interface contract for the telephony gateway |

### 8.3 Existing machinery

The re-enrolment audit (`auditReenrolment`) is keyed by subscriber, so it
would apply to email and Google sign-ins too, and inherits the 25-hour
detection limit found in `SIM-SWAP-CONTROLS-REVIEW.md` §2.

**Approvers:** Architecture team; Security; the owner (decisions 9, 10, 11).

---

## 9. Account deletion, erasure and the tombstone conflict

### X2. Tombstone or erase the new identifiers? (with INV-ID-7, L2 and D1/O9)

- **Current state:**
  - In-app deletion is method-agnostic (refresh token plus DPoP proof,
    `internal/app/erasure.go:115-185`); ADR-0032 keeps it.
  - Erasure would delete every identifier row, an encrypted address (B),
    email challenges and their rate-limit state, in one transaction (D32.7).
  - Access tokens are not revoked at deletion; up to 15 minutes remain
    (`erasure.go:41-44`, D9/O17).
  - Web deletion is SMS-based and dormant; ADR-0032 proposes an email code
    for email accounts and web Google sign-in for Google-only accounts, both
    under D5/O13.
- **Why needed:** INV-ID-7 says erased identifiers are tombstoned and never
  reused. A tombstone of an email or Google identifier **is** its pseudonym,
  which is personal data the erasure was meant to remove.
- **Options:**
  - **(a)** Erase the pseudonyms. The same address or Google account can
    register again as a new account. INV-ID-7 is narrowed to the subscriber
    id (random, never reused).
  - **(b)** Keep a tombstone pseudonym for a defined period under a legal
    basis (for example abuse prevention), then delete it.
  - **(c)** Keep tombstones indefinitely. Hard to justify under purpose
    limitation.
  - **Also:** whether the email **suppression list** entry survives erasure.
    It is keyed by the address pseudonym, not the account.
- **Security and privacy consequences:** (a) allows unlimited fresh accounts
  per address after deletion (abuse); (b) and (c) retain personal data after
  an erasure request.
- **Dependencies:** S3; L2 and L3; owner decision 12 (D1/O9 re-registration,
  D4/O12 step-up, D5/O13 web deletion, D9/O17 token revocation, OD-8
  frontend).
- **Evidence for approval:** a legal opinion on (b)'s basis and period; an
  updated erasure-store inventory; a test that erasure leaves no identifier
  row, challenge or rate-limit entry behind.
- **Approvers:** privacy and legal; the owner; Architecture team (INV-ID-7).

---

## 10. On-device screening without authentication

### X3. Does basic on-device screening require sign-in? (owner decision 8)

- **Current state, verified in code:** the Android screening service
  (`core/telephony/.../screening/PreFilterScreeningService.kt`) uses only the
  local caller lists, a contacts lookup and the pre-filter. **It has no
  dependency on a session, identity or the network.** Onboarding today
  stops at phone verification.
- **Why needed:** the answer decides whether the app collects an email
  address before it delivers any value, and what an identity outage breaks.
- **What needs what** (ADR-0032 D32.5a):

| Function | Runs on | Account? | Verified number? |
|---|---|---|---|
| Holding the screening role; Stage 1 pre-filter; local block and allow lists | Device | No (proposed) | No |
| Consent records for cloud processing; deletion of server data | Server | Yes | No |
| Call-list sync; forwarding to a DID and cloud screening; transcripts; elevated-risk operations | Server and telephony | Yes | **Yes** |

- **Options:** **(a)** local screening without sign-in; ask at the first
  cloud feature (ADR-0032's recommendation); **(b)** sign-in always required.
- **Security and privacy consequences:** (a) collects no address without a
  purpose (ADR-0012 purpose limitation) and survives an identity or email
  outage, but local lists are not backed up until sign-in, and the sign-out
  copy must say local screening continues. (b) is simpler to reason about
  and collects data earlier.
- **Dependencies:** A6 (onboarding screens); the Data safety form (L5).
- **Evidence for approval:** the onboarding flow for (a); confirmation that
  no local function reads a session (a test or lint rule keeping
  `core/telephony` free of `core/auth`).
- **Approvers:** the owner; Architecture team (A6); privacy review.

---

## 11. Security risks and mitigation requirements

| # | Risk | Mitigation required before implementation | Card |
|---|---|---|---|
| R1 | **Takeover through a compromised mailbox or Google account** (replaces SIM swap as the primary account threat) | ADR-0010 §10 analogue: notice to existing devices, cooling-off before number-dependent changes, re-verification of elevated-risk operations, "sign out everywhere" | A9, X1, owner 11 |
| R2 | **Real-time code phishing:** an attacker requests a code for the victim's address and talks them into reading it out | Message text: purpose, "never share this code", no links; the code is bound to the requesting device's DPoP key, so it signs in the attacker's device only, and R1's notice fires | D32.2 |
| R3 | Phone gate bypass | P1–P8 rules and their tests | A5, A9 |
| R4 | Account takeover by identifier matching | Never link by email; uniqueness per `(kind, pseudonym)`; fresh proof to link | A7, D32.4 |
| R5 | Google token for another app, or replayed | `aud` = our server client id; nonce bound to the DPoP key thumbprint; token hash reserved in the replay store until expiry | A4 |
| R6 | Google key fetch failing or poisoned | Fail closed; unknown `kid` refused; keys only from Google's endpoint over TLS | A4 |
| R7 | Account enumeration | The same answer whether or not an account exists; 503 and 429 are account-independent; suppressed addresses get the same answer | D32.2 |
| R8 | Mail bombing and cost | Per-address limits, a global hourly send budget, per-source limits (C13, blocked on E6) | owner 6 |
| R9 | Code guessing; lockout as denial of service | 6 digits, single use, HMAC-stored, 5 failures then 15 minutes; the lockout of a victim's address is accepted as for SMS today | owner 6 |
| R10 | Spoofed mail from our domain carrying fake codes | SPF, DKIM, DMARC tightening to `reject` | A8, owner 7 |
| R11 | A static cloud credential introduced for email | SES API through IRSA only; no SMTP; a startup refusal of a configured key | A8 |
| R12 | Pseudonym key compromise: offline testing of guessed addresses | A separate key purpose, keyring rotation, the key never logged | A7 |
| R13 | Stale phone-ownership state in tokens | A bounded window (15 minutes); recheck for elevated-risk operations | A5 |
| R14 | Lost single method means a lost account | Encourage linking a second method; accept or fund recovery (owner decision 15) | owner 15 |
| R15 | Sender reputation: the provider may limit or pause sending on high bounce or complaint rates | Bounce and complaint suppression, rate alerts; confirm the provider's thresholds | A8 |
| R16 | The SMS route re-enabled by configuration alone | A guard that refuses SMS sign-in while email or Google sign-in is enabled | A9 |

---

## 12. Exact approvals required before implementation

**Nothing below is approved.** Each gate must be recorded (in the ADR's
status, or a linked record) before the work it gates starts.

### 12.1 Ordered gates

| Gate | Approval | By | Gates |
|---|---|---|---|
| G0 | How architecture-team approval is recorded | **Resolved:** P2 adopted by the owner in #199 (`cef3695`) | Everything |
| G0a | **An independent human reviewer is identified** (§6.1, rule 8) | Owner | Every high-risk approval below |
| G1 | **A1:** review trigger ruling | Architecture team | Everything below |
| G2 | **O2:** ADR-0030 D30.3 ratified, and **A7** amendments | Owner; Architecture team; Security | Any non-number account |
| G3 | **A2** supersession of ADR-0010 §5(a), with **A3**/S1–S4 invariant sign-offs and **A6** specification updates, in one pull request | Architecture team; Security; owner; privacy and legal (S3, S4) | Accepting ADR-0032 |
| G4 | Privacy and legal: L1 (including breach intimation, X1), L2/L3 (X2), L4, L5, residency and DPA | Privacy and legal | Collecting any email address |
| G5 | **A8** SES security shape, and owner decision 7 (provider, region, domain) | Architecture team; Security; SRE; owner | Email sender |
| G6 | Owner decisions 5 (X1), 6 (code parameters), 8 (X3), 11 (R1 controls), 15 (recovery) | Owner | Email sign-in |
| G7 | **A4** verifier library ADR; owner decision 3 (package name, Google Cloud project) | Architecture team; Security; owner | Google sign-in |
| G8 | **A5** and **A9**; owner decisions 9 and 10 | Architecture team; Security; owner | Any number-dependent feature; SMS's return |
| G9 | Owner decisions 4 (linking) and 12 (deletion decisions D1, D4, D5, D9, OD-8) | Owner | Linking; web deletion |
| G10 | Owner decisions 13 (OD-1, Play reviewer path) and 14 (OD-10, Phase 19 criteria) | Owner | Release planning |

### 12.2 By workstream

| Workstream | Needs |
|---|---|
| Email sign-in (backend and Android) | G0a–G6 |
| Google sign-in | G0a–G4, G6, G7 |
| Linking | Email or Google sign-in, plus G9 |
| Web deletion for email and Google accounts | G9, plus OD-8 |
| Number-dependent features and the SMS return | G0a, G8, plus the SMS provider and DLT (E2) |

### 12.3 Owner decisions referenced (ADR-0032 `:890-922`)

1 ratify the direction · 2 O2 · 3 package name and Google Cloud project ·
4 linking · 5 address design (X1) · 6 code parameters · 7 provider, region
and domain · 8 local screening (X3) · 9 phone-ownership mechanism (A5) ·
10 a number verified elsewhere · 11 compromise controls · 12 deletion
decisions · 13 OD-1 · 14 OD-10 · 15 recovery.

### 12.4 External setup (none started; costs unverified; not to be started without the gates)

- **Email:** a provider account, a verified sending domain and its DNS
  records, the IRSA role, the event destination, and SES production access.
- **Google:** a Cloud project, the OAuth consent screen, Android, server and
  later web client ids (the Android client is tied to the package name and
  the Play App Signing certificate), and egress to Google's keys.
- **SMS:** unchanged, deferred (E2).

---

## Decision register

| Id | Decision | Section | Approvers |
|---|---|---|---|
| A1 | Review trigger | §3 | Architecture team |
| A2 | Supersede ADR-0010 §5(a) | §3 | Architecture team, Security, owner |
| A3 | Invariant sign-off (S1–S4) | §3, §4 | Architecture team (+ privacy and legal for S3, S4) |
| A4 | Verifier library ADR | §3 | Architecture team, Security |
| A5 | Phone-ownership mechanism | §3, §8 | Architecture team, Security, owner |
| A6 | Domain and UX specifications | §3 | Architecture team, owner |
| A7 | ADR-0030 amendments | §3, §5 | Owner, Architecture team, Security |
| A8 | SES security shape | §3, §7 | Architecture team, Security, SRE, privacy and legal |
| A9 | SMS route redesign; sign out everywhere | §3, §8 | Architecture team, Security, owner |
| S1 | INV-ID-1 | §4 | Architecture team, owner |
| S2 | INV-ID-2 | §4 | Architecture team, Security, owner |
| S3 | INV-ID-7 | §4, §9 | Architecture team, privacy and legal, owner |
| S4 | INV-ID-8 (I2) | §4 | Architecture team (frozen-invariant sign-off), privacy and legal |
| X1 | Address design A or B | §6 | Owner, privacy and legal, Security, Architecture team |
| X2 | Tombstone or erase identifiers | §9 | Privacy and legal, owner, Architecture team |
| X3 | Local screening without sign-in | §10 | Owner, Architecture team, privacy review |

**16 decisions. None is approved.**

## Sources

**Repository:** ADR-0008 (`:73`, `:180`); ADR-0010 (`:14`, `:65-69`,
`:108-112`, `:161-167`, `:183-187`, `:207-217`, `:304`, `:308-320`);
ADR-0012 (C4, C5, C8); ADR-0029 (`:49`, `:106-108`, `:129-132`,
`:140-141`); ADR-0030 (`:83-89`, `:92-113`); ADR-0032;
`ARCHITECTURE_FREEZE.md` (`:64`, `:115`, `:119-150`); `DOMAIN_FREEZE.md`
(`:207-208`); `docs/domain/10-identity.md` (`:18`, `:72-75`, `:441-450`);
`docs/phase19/ADR-0030-DECISION-BRIEF.md`;
`docs/phase19/ADR-0030-D30.3-MIGRATION-PLAN.md` (`:107-125`);
`docs/phase19/SIM-SWAP-CONTROLS-REVIEW.md`; `.github/CODEOWNERS`.

**External** (read 2026-10-31; nothing created or contacted):
- AWS, Obtaining Amazon SES SMTP credentials:
  <https://docs.aws.amazon.com/ses/latest/dg/smtp-credentials.html>
- AWS, Request production access (the SES sandbox):
  <https://docs.aws.amazon.com/ses/latest/dg/request-production-access.html>
- AWS, Amazon SES endpoints and quotas:
  <https://docs.aws.amazon.com/general/latest/gr/ses.html>
- Google, OpenID Connect:
  <https://developers.google.com/identity/openid-connect/openid-connect>
- Google, Authenticate with a backend server:
  <https://developers.google.com/identity/sign-in/android/backend-auth>
