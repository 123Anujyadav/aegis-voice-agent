# ADR-0032: Additional authentication methods: passwordless email code (primary) and Sign in with Google (alternative)

- **Status:** **Proposed: high-risk; awaiting independent human review. Not
  accepted, not ratified, not implemented.**
  - **High-risk** under `docs/adr/README.md`, "Approval": it would supersede
    part of an Accepted ADR (ADR-0010 §5(a)), change identity invariants, the
    authentication model and personal-data collection. That is the author's
    proposed classification; the final classification is the owner's
    (`ARCHITECTURE_FREEZE.md` §6.1, rule 6).
  - **No independent human reviewer is available.** Under §6.1 rule 10 this
    ADR therefore stays Proposed, and it has no effect on implementation
    (rule 7).
  - It records the repository owner's **product direction** of 2026-10-31:
    1. passwordless email one-time code (6 digits) as the primary sign-in;
    2. Sign in with Google as an optional alternative;
    3. SMS one-time codes **deferred** to a later phase, not cancelled.
  - It evaluates that direction against the accepted architecture and lists
    what each part would require. **It decides nothing on behalf of the
    architecture team, privacy and legal review, or the owner's open
    decisions** (§"Approvals and open decisions").
  - **ADR-0010 is Accepted and is not modified by this document.** Where this
    proposal departs from it, the departure is stated as a change that would
    need a superseding decision. **No invariant is declared approved, changed
    or superseded here.**
- **Date:** 2026-10-31. Revised the same day after an **AI-performed review**
  (Claude Code, 2026-10-31), which is not an independent human review
  (§6.1, rule 9). Its corrections are marked "corrected" or "added in
  review" where they appear, and **none of them decides an open item.**
- **Deciders (none recorded yet):** the repository owner
  (`ARCHITECTURE_FREEZE.md` §6.1, rule 6).
- **Independent human review:** required (§6.1, rule 8); **not yet
  available.** See "Approval record".
- **Also required:** privacy and legal review (DPDP, residency, notices).
- **Drafted by:** Claude Code, the delegated Technical Lead, at the owner's
  direction. Drafting is not a decision (§6.1, rule 7).
- **Consulted:** Android; Security; SRE (email provider, DNS, egress, Google
  Cloud project)
- **Informed:** Support (account recovery and deletion requests)
- **Would affect, if accepted:**
  - ADR-0010 §5(a), the identity primitive. A superseding decision is needed.
  - ADR-0029, only if a phone-ownership claim is added to the access token
    (D32.5).
  - ADR-0030:
    - D30.3 is a prerequisite (random subscriber ids). As written, it keys
      the id table by the **number's** pseudonym, so it would need
      generalising to several identifier kinds (D32.1).
    - D30.1 and D30.2 would need new key purposes for the email and Google
      lookup pseudonyms (D32.1).
  - ADR-0012, for new personal data and residency.
  - ADR-0008: unchanged. The email sender must follow its "no static cloud
    credentials" rule (D32.8).
  - ADR-0031: unchanged. Play Integrity binds to the DPoP proof whatever the
    sign-in method.

---

## Context

### What is accepted today

**ADR-0010, Accepted (2026-08-06):**
- **The identity primitive is the phone number:** "The subscriber's identity
  in this product **is their phone number**"
  (`docs/adr/0010-authentication-and-device-trust.md:14`).
- **Sign-in is an SMS code** at enrolment and re-verification (`:65-69`).
- **The device key binds the session:** a hardware-bound proof-of-possession
  key, with Play Integrity at issuance.
- **A hosted identity provider was rejected** (`:161-167`):
  - for **residency**;
  - for **fit**: "our identity primitive is an MSISDN with a hardware-bound
    device key and a consent record, not an email/password account".

**The freeze and the domain:**
- `ARCHITECTURE_FREEZE.md` is FROZEN.
  - Its summary row reads "Auth | MSISDN + OTP · Keystore PoP · Play Integrity
    · EdDSA JWT" (`:115`).
  - Its rules (`:140-149`):
    - an accepted ADR is never edited, only superseded;
    - a superseding ADR needs architecture-team approval;
    - a review trigger must have fired;
    - invariants need explicit sign-off, not merely a superseding ADR.
- `docs/domain/10-identity.md` anchors the `Subscriber` on the number (`:18`).
  Its invariants are:
  - INV-ID-1: "A Subscriber has exactly one active MSISDN" (`:441`);
  - INV-ID-2: "An MSISDN maps to at most one active Subscriber" (`:442`);
  - INV-ID-7: erased identifiers are tombstoned and never reused (`:447`);
  - INV-ID-9: no `ACTIVE` subscriber with zero trusted devices (`:449`);
  - INV-ID-10: "There is no password, passphrase or knowledge factor anywhere
    in this context" (`:450`).
- `DOMAIN_FREEZE.md` and `UX_FREEZE.md` are still PROPOSED.

### What the code does today (`main` `7f6c8ee`)

**Sign-in machinery, all reusable:**
- **Challenges:** a one-time-code challenge store, with codes held only as
  HMACs and a `purpose` column (migration 0009).
- **Rate limits and attempts:** a per-identifier policy of 3 requests and 5
  failures per 15 minutes, then a 15-minute lockout
  (`services/go/identity/internal/app/identity.go:974`); a shared code check
  (`checkCode`); a 5-minute code lifetime (`:1064`).
- **Delivery:** a provider-neutral failover sender (`internal/otpdelivery`).

**Session security (ADR-0029, ADR-0031), which this proposal must not
weaken:**
- **Device binding:** a DPoP proof is verified at every issuance, and the
  session is bound to the device key's thumbprint. The binding can never be
  changed.
- **Tokens:** EdDSA `at+jwt` access tokens (15 minutes) carrying `cnf.jkt`.
  Refresh tokens rotate, are stored hashed, and have reuse detection.
- **Play Integrity:** the verdict is bound to `SHA-256(DPoP proof)`.

**Deletion:**
- **In-app deletion** needs the refresh token plus a DPoP proof
  (`internal/app/erasure.go:115-185`). It does not depend on how the session
  was obtained.
- **Web deletion** is SMS-based and dormant (`internal/app/web_erasure.go`).
- **Access tokens are not revoked** on deletion; they stay valid for up to 15
  minutes (`erasure.go:41-44`, open decision D9/O17).

**The account is the number:**
- **The subscriber id is derived from the number:** `usr_` + HMAC(MSISDN)
  (`internal/app/subscriber_directory.go:24-32`).
- **There is no account table.** An account is the set of sessions sharing a
  `subscriber_id`.
- **No email, external subject or provider column** exists in any migration
  (0001–0009).

**Production today:**
- **No SMS provider is wired,** so nobody can sign in.
- **On Android,** onboarding stops at phone verification.
- **What sign-in unlocks:** account deletion only. The edge-api client is
  built but never injected (`android/app/.../di/IdentityModule.kt:85-89`), and
  nothing in production uses the access token.

### What the owner's direction changes

- **The account would no longer be the number.** An email address or a Google
  account cannot derive today's subscriber id, and neither proves ownership of
  a phone number.
- **The product still needs a proven number** for anything keyed to it:
  - call forwarding and the forwarding DID (ADR-0002, ADR-0010 §10);
  - the telephony gateway's subscriber association;
  - server-side call-list sync (OD-2).
- **So the direction separates two things that ADR-0010 combined:**
  1. *who the account holder is*: the email or Google identity;
  2. *which phone number they have proven they control*: a capability, later
     proven by SMS or silent carrier verification.

### Governance fact that must be resolved first

ADR-0010's review triggers (`:312-320`) include "Expansion to a market where
SMS OTP is not viable or where a different identity primitive is
conventional".

**No trigger has been shown to have fired.** The owner's direction is a
product decision. Whether a product decision is a sufficient trigger under
`ARCHITECTURE_FREEZE.md` rule 2 (`:142-143`) is **for the architecture team
to decide.** This ADR cannot assert it.

## Decision Drivers

In priority order:

1. **No weakening of device-bound sessions.** Every issuance stays DPoP-bound,
   with no bearer session from any sign-in method (ADR-0010 `:108-112`;
   ADR-0029).
2. **Honest proofs.**
   - An email code proves control of a mailbox.
   - A Google ID token proves a Google account.
   - **Neither proves a phone number,** and nothing may treat it as if it did.
3. **No account takeover through identifier matching.** Never link, merge or
   look up accounts by email address alone.
4. **Privacy and residency** (ADR-0012, DPDP): collect the minimum, keep the
   identity store in India, pseudonymise identifiers as the number is today.
5. **Deletion and erasure invariants keep holding,** including for the new
   identifiers.
6. **Reuse the tested machinery** (challenges, rate limits, delivery seam,
   session issuance) rather than build parallel paths.
7. **Reversible and additive.** SMS returns later as phone-ownership
   verification, without migrating accounts again.
8. **A working sign-in for Play review** without any production bypass
   (OD-1).

## Considered Options

1. **Keep ADR-0010 as accepted:** SMS-only, number as identity. Nobody can
   sign in until an SMS provider and DLT registration exist.
2. **Email code as primary, Google as alternative, phone ownership as a
   separately proven capability.** Both methods issue the existing
   DPoP-bound session. *(Owner's direction.)*
3. **Google sign-in only.**
4. **An email magic link instead of a code.**
5. **A hosted identity provider** (for example Firebase Authentication or
   Amazon Cognito) running the email and Google flows.
6. **Email and password.**

## Decision Outcome (proposed)

**Proposed: Option 2,** subject to every approval and open decision listed
below. Each numbered sub-decision states what is proposed and what it
depends on.

### D32.1 The account becomes a stored random subscriber id

**Two kinds of identity, never confused:**

| | Internal account identity | External identifiers |
|---|---|---|
| **What** | The subscriber id: a random `usr_` value | Proofs of something outside: a mailbox, a Google account, later a phone number |
| **Created** | Once, at the account's first successful sign-in | When proven: at sign-in, or at an explicit link (D32.4) |
| **Changes** | Never, for the account's life | May be added and removed. The account keeps its id. |
| **Appears in** | The access token's `sub` (ADR-0029); every other service's subscriber reference | **identity only:** never in a token, an event or another service |
| **Stored as** | The id itself | A pseudonym: an HMAC under the pseudonym key, never the raw value |

- **The proposal:**
  - A subscriber id becomes a random `usr_` value created at the account's
    first successful sign-in, by whichever method.
  - It is **never derived** from an email address, a Google subject or a
    number. Unlinking an identifier, or a provider changing it, cannot change
    the id.
  - Each external identifier is one row mapping
    `(kind, key id, pseudonym) → subscriber id`, where kind is one of:
    - `email`: the pseudonym of the normalised address (D32.2);
    - `google`: the pseudonym of the **issuer and subject together**
      (`iss` + `sub`). A Google `sub` is unique only within its issuer, and
      the email in the token is never part of the key (D32.3);
    - `msisdn`: a **verified** number only (D32.5).
  - A pseudonym is unique per kind (a uniqueness constraint), so an
    identifier belongs to at most one account. That is the rule D32.4's
    "no merge" relies on.
  - Signing in looks up `(kind, pseudonym)`. A hit returns that subscriber id;
    a miss creates a new subscriber id and its first identifier row in one
    transaction.
- **Prerequisite:** ADR-0030 D30.3 (owner decision O2), which proposes stored
  random ids. Without it, a non-number account has no id to derive.
  - **D30.3 as written is not sufficient.** Its table is
    `identity_subscribers(pseudonym, subscriber_id)`, keyed by the number's
    pseudonym, and it creates the id "at the number's first successful
    enrolment" (`0030-identity-hmac-key-separation-and-rotation.md:92-98`).
    This proposal needs it generalised to the identifier rows above. That is
    an amendment to D30.3, for the owner and the architecture team.
- **Keys (ADR-0030 D30.1, D30.2):**
  - D30.1 decides one key per purpose. The email and Google lookup pseudonyms
    are a **new purpose**: they key an account for its whole life, unlike
    today's number pseudonym, which keys rate limits for minutes.
  - Whether they share the subscriber-lookup key of D30.3 or get their own
    is for the architecture team.
  - **Rotation is lazy,** as D30.2 already proposes for the number
    (`:83-89`): the identifier is presented again at each sign-in, so its
    row can be re-keyed then. An account whose holder does not sign in during a
    rotation stays under the previous key, so **a lookup key's previous
    version cannot be removed on a timer.** The retirement rule is an open
    architecture item.
- **Effect on invariants** (stated, **not** declared):
  - INV-ID-1 would become "at most one verified MSISDN".
  - INV-ID-2 would apply to *verified* numbers only.
  - Both need sign-off.
  - INV-ID-10 is **not** affected: email codes and Google sign-in are
    possession and federation factors, not knowledge factors. No password is
    introduced.

### D32.2 Passwordless email code: the primary method

**Flow:**
- `POST /v1/email/code/request` with `{email}`:
  - **The same answer whether or not an account exists** (no enumeration).
    Sign-up and sign-in are the same flow, so a code is sent either way and
    the work done does not depend on the account.
  - A suppressed address (D32.8) gets the same answer, and no message.
  - Corrected from "always 202": **a delivery failure** (every provider
    failing, or the request ending) is answered as SMS is today, with
    `ERR_OTP_DELIVERY_FAILED` (503) and the challenge withdrawn (#198). That
    reveals nothing about accounts. A 429 from the rate limit is likewise
    account-independent.
  - **A provider accepting a message is not delivery.** Bounces arrive later
    and asynchronously (D32.8).
- `POST /v1/email/code/validate` with `{requestId, code}`, a **DPoP proof**,
  and optionally `Integrity-Token`:
  - It issues exactly the existing session: the `cnf.jkt` binding, the
    ADR-0029 access token, a rotating refresh token, and the ADR-0031
    integrity tier.
  - Route names are illustrative. The final ones belong to the
    implementation PR.

**The address:**
- **Accepted form:** trimmed, ASCII only at first, and lower-cased for
  lookup.
- **No alias folding:** dots and `+tags` are not removed, because folding
  would merge distinct mailboxes.
- **Internationalised addresses** are an open decision.
- **The lookup key is a pseudonym:**
  `HMAC(pseudonym key, "callscreen.identity.email.v1\0" + address)`,
  domain-separated as the number pseudonym is (key purpose: D32.1).
  - In a request, the raw address is handed to the email sender and nowhere
    else, as the number is today. It never appears in a log, an error, an
    event or a token.

**Whether a recoverable address is also stored: open decision 5.** The
pseudonym is one-way, so on its own it can find an account but **cannot be
mailed.** The two designs:

| | **A. Pseudonym only, the user supplies the address each time** (proposed default) | **B. Pseudonym plus an encrypted address** |
|---|---|---|
| **Sign-in and sign-up codes** | Work: the user types the address, which is used for that one message | Work |
| **Web deletion by email** | Works: the user types the address on the web page | Works |
| **Mail we start ourselves:** new-sign-in alerts, a "this wasn't me" link, deletion confirmation, breach notices | **Impossible.** Only in-app or FCM channels to the account's own devices remain | Possible |
| **Recovery** | The holder proves the mailbox again: type the address, receive a code. **"Which address did I use?" cannot be answered,** by support or anyone. | The same proof, plus support could confirm a masked hint to a proven holder |
| **A database leak** | Pseudonyms only. Useless without the pseudonym key, which is held separately | Ciphertexts. Useless without the encryption key, which must be held in KMS (`ap-south-1`) under a separate IAM permission |
| **Key loss or compromise** | Lookup key compromise allows guessing known addresses offline. Rotation is lazy (D32.1). | The same for the lookup key, plus the encryption key's custody and rotation |
| **Erasure** | Delete the row | Delete the row; backups hold ciphertext until they expire (ADR-0012 C5) |
| **Data inventory and Data safety** | Email "collected" (it is processed in each request), not retained | Email collected and retained |

- **C. A plaintext address column** is rejected: it adds nothing over B and
  removes the separate-key protection.
- **The trade-off is the compromised-mailbox control.** Under A, D32.6's
  notice to the holder can reach only their devices, by push, and never
  their inbox. An attacker who signs in from a new device is announced only
  on devices the holder still has. **Push is not built today:** no FCM
  dependency exists (`android/core/notifications/build.gradle.kts` only
  names it in a comment), and identity sends no notifications.
- **Breach intimation:** ADR-0012 C8 records DPDP §8(6)'s notification duty.
  Whether in-app notice satisfies the duty to inform each affected person
  when no address is held is a **legal question (L1)**, not decided here.
- **The proposal's default is A,** for minimisation. Choosing B is the
  owner's decision with privacy review, and it needs the KMS key design.

**The code:**
- 6 digits from `crypto/rand`, stored as an HMAC only, single use.
- The shared attempt guard applies.
- **Lifetime:** today's is 5 minutes. Email is often slower, so 10 minutes is
  proposed (open decision 6).
- **A new challenge purpose,** `email_sign_in`, so a code can never be used
  for another purpose. The purpose binding already exists from #193.

**Rate limits:**
- **Per address pseudonym:** the existing per-identifier policy.
- **A global hourly send budget,** modelled on the deletion-code budget (#193),
  to bound mail bombing and cost. The value is the owner's or SRE's.
- **No per-source limit yet:** that is roadmap task C13, blocked on E6.

**The message:**
- The code appears **in the body only, never in the subject**, because
  subjects show in notification previews and lock screens.
- It carries no links and no images.
- It includes an "if you did not ask for this, ignore it" line.
- The provider's **open and click tracking is disabled.**

**Delivery:**
- A provider-neutral email sender, with the `otpdelivery` semantics:
  - an ordered failover;
  - a per-attempt bound;
  - abandonment when the request ends;
  - withdrawal of undelivered codes (#198);
  - no provider text in errors;
  - per-provider health;
  - a `…_deliveries_total{provider,outcome}` metric.
- Provider-specific code waits for the provider choice.

### D32.3 Sign in with Google: an optional alternative

**Client:**
- Android Credential Manager with the Google ID option (Google's current
  recommendation; the legacy One Tap and Sign-In APIs are deprecated in its
  documentation).
- On devices without Google Play services, Google sign-in is simply not
  offered, and the email code remains.

**Server-side verification, mandatory.** Per Google's backend guide:
- **Signature** against Google's published keys, fetched and cached, with
  unknown `kid` values refused. **A key-fetch failure fails closed.**
- **`aud`** equals our server client id exactly. This blocks tokens minted
  for another app.
- **`iss`** is Google's issuer. Google documents two spellings
  (`https://accounts.google.com` and `accounts.google.com`); both are
  accepted and **normalised to one canonical value** before the pseudonym
  is computed, or one person would get two accounts.
- **`exp` and `iat`** are checked with a bounded skew.
- **The `nonce` matches the binding below.**

**Library: its own ADR.** ADR-0029 decided that widening the token profile,
including "remote JWKS fetching", means adopting "a maintained library
through its own ADR, rather than extending the hand-written parser"
(`0029-access-token-and-proof-of-possession.md:106-108`). Google tokens are
RS256 with remote keys, so **the verifier library is a separate dependency
ADR.** This proposal does not choose it.

**Binding the Google token to the device key.** Recommended, and to be
confirmed by security review:
- The app generates a random value `r`. It requests the Google ID token with
  `nonce = base64url(SHA-256("callscreen.identity.google-nonce.v1\0" + jkt + "\0" + r))`.
- It sends `{idToken, r}` with a DPoP proof to the exchange endpoint.
- The server recomputes the nonce from the **verified proof's thumbprint**,
  then reserves the token's hash in the existing replay store until its
  expiry, making each token single-use.
- **Why not hash the proof itself, as ADR-0031 does:** the account-chooser UI
  can outlast the proof's 2-minute freshness window. Binding to the key
  thumbprint avoids that, and the proof still covers the request itself.

**What identifies the account** (corrected; see D32.1):
- **The account is the internal subscriber id,** never a Google value. The
  token's `sub` is not the account key.
- **The Google identifier** is the pair `(iss, sub)`, stored only as a
  domain-separated pseudonym
  (`"callscreen.identity.google.v1\0" + iss + "\0" + sub`) in an identifier
  row pointing at the subscriber id. Google documents `sub` as unique and
  never reused for a Google account; the issuer is included so that the key
  stays unambiguous if another issuer is ever accepted.
- **Google values never leave identity:** not in our access token (whose
  `sub` is the internal id, ADR-0029), not in an event, not in a log.
- **`aud` and `azp`:** `aud` must be our **server** client id. Google
  describes `azp` as the authorised presenter's client id, present when the
  requester differs from the audience, as with an Android app requesting a
  token for its server. Whether `azp` must also equal our Android client id
  is for security review.
- **The token's `email` is not an identifier.** It is not used for lookup or
  linking, and is **not stored**, under either design of open decision 5:
  only an address proven by an email code is ever kept (D32.2). Google's
  OpenID Connect page warns that it "may not be unique to this account and
  could change over time".
- **`email_verified` is never treated as authoritative:**
  - Google's guidance is that for non-Gmail addresses without the `hd` claim,
    Google is not authoritative for the address.
  - A true value also reflects past, not current, control.

**Network:** egress from identity to Google's key endpoint is a
network-policy change for SRE. A Google outage disables Google sign-in only.

### D32.4 Linking: never by email; explicit and authenticated only

- **No automatic linking or merging,** in either direction. An email-code
  account for `a@x` and a Google account whose token says `a@x` are
  **different accounts** unless the holder links them.
- **Linking, if offered,** happens from a session already authenticated by
  method A (DPoP-bound), plus a **fresh** proof of method B in the same
  request: a new email code to that address, or a fresh Google ID token.
- **Refused cases:**
  - Linking an identifier already attached to another account. **No merge.**
  - Removing the last sign-in method.
- **Whether linking ships in the first release** is open decision 4. Without
  it, a person who uses both methods has two accounts.
  - Only one of the two can later verify a given number (INV-ID-2, D32.5).
  - Deleting one does not delete the other. Each is erased separately
    (D32.7).
  - A linked identifier is a second way into the **same** account, and so a
    second takeover path. Linking widens recovery and attack surface
    together (D32.6).

### D32.5 Phone ownership is a separate capability, still gated

- **A subscriber gains a verified number only through an independent
  ownership proof:** SMS one-time code (deferred) or silent carrier
  verification (ADR-0010 option 5).
- **These never verify a number:** an email code, a Google token, or a number
  typed into the app.
- **The gate is enforced by the server, not only the UI.** It applies to:
  - every number-dependent endpoint: forwarding provisioning (C8), DID
    routing, call-list sync (C7) and the telephony gateway's association;
  - ADR-0010 §10's elevated-risk operations.

  Today no such endpoint exists (edge-api serves only `GET /v1/me`), so this
  is a binding rule for every future one.

**Bypass analysis.** Each path by which an email or Google sign-in could
reach a number-dependent feature, and the rule that closes it:

| # | Path | Rule |
|---|---|---|
| P1 | The session itself: any valid access token | A token proves the account and the device, never a number. Every number-dependent endpoint checks the **verified-number state**, not merely a valid token. |
| P2 | A number the client supplies: typed, read from the SIM, or pre-filled (UX A02) | Never trusted. A number-dependent operation uses only the server's verified number for the account, and **ignores any number in the request**. |
| P3 | A phone number inside a Google token or profile | Not requested, not read, not stored. |
| P4 | **Re-enabling today's SMS sign-in route unchanged** (`/v1/otp/validate`, `ValidateOtp` in `internal/app/identity.go`) | Today that route **creates a session for an account derived from the number.** Re-enabled beside email accounts, it would create a second, number-keyed account, and that account would hold the number implicitly. **It must not be re-enabled as sign-in while this proposal is in force.** It becomes "verify a number for the signed-in account" (below), which needs a design change, not reuse "unchanged". |
| P5 | Linking (D32.4) | Linking attaches only email and Google identifiers. A number is attached only by the verification step. |
| P6 | Account recovery | Recovering an account by email or Google returns its verified number only as it is, and does **not** refresh verification. The ADR-0010 §10 analogue (cooling-off, notice) applies to the number-dependent features after any new-device sign-in (D32.6). |
| P7 | A stale state in a token (only under option (a) below) | A token minted before the number was removed stays valid up to 15 minutes (D9/O17). Elevated-risk operations recheck at identity. |
| P8 | Another service trusting `sub` alone | The telephony gateway and every future consumer associate a number with a subscriber **only through identity's verified record**, never by matching a token or a client value. |

**How edge-api learns the state** is open decision 9:
- *(a)* **In the access token.** Corrected: this has two forms.
  - A **new claim** changes ADR-0029's exact claim set (`:49`), so ADR-0029
    must be amended.
  - A **role value** fits the existing `roles` claim, which ADR-0029 leaves
    to product policy (`:140-141`). It changes no claim set, but it is still
    authorisation state in a self-contained token, and needs
    architecture-team review.
  - Either way, identity recomputes it at every issuance and refresh, and
    the token can lag a removal by up to 15 minutes (P7).
- *(b)* **A lookup at identity.** It is always current, but it puts identity
  in the request path, against ADR-0010 §9's local verification
  (`:183-187`, `:304`).
  It may be acceptable for rare elevated-risk operations only.

**When SMS returns:**
- INV-ID-2 is enforced at that point.
- A number already verified on another account is refused, with no merge
  and no transfer (open decision 10).
- The dormant SMS challenge machinery (challenges, rate limits, delivery
  seam, deletion template) is reused, but **the validate step changes:** it
  attaches a verified number to the authenticated account instead of
  issuing a session for a number-derived account (P4).

### D32.5a What needs an account, and what does not (input to open decision 8)

**Verified in the code:** the Android screening service
(`core/telephony/.../screening/PreFilterScreeningService.kt`) uses only the
local caller lists, the contacts lookup and the pre-filter. **It has no
dependency on a session, identity or the network.**

| Function | Where it runs | Needs an account? | Needs a verified number? |
|---|---|---|---|
| Holding the call-screening role | Device | No | No |
| Stage 1 pre-filter: contacts ring through, local block and allow lists | Device | **No** (proposed) | No |
| Consent records for cloud processing (ADR-0012, kept in identity) | Server | Yes | No |
| Account deletion of server data | Server | Yes | No |
| Server-side call-list sync (C7, OD-2) | Server | Yes | **Yes** |
| Carrier forwarding to a DID, and cloud screening of forwarded calls (ADR-0002) | Server and telephony | Yes | **Yes** |
| Transcripts, summaries, and anything derived from forwarded calls | Server | Yes | **Yes** |
| ADR-0010 §10 elevated-risk operations | Server | Yes | **Yes**, and re-verification |

- **Recommendation, for the owner to decide (open decision 8):** basic
  on-device screening works **without sign-in**. Sign-in is asked for at
  the first cloud feature.
  - **Why:** nothing local reads the server; requiring an account for
    local-only functions would collect an email address with no purpose,
    against ADR-0012's purpose limitation; and an identity or email outage
    would not stop local screening.
  - **Consequences if chosen:**
    - the onboarding specification (screens A02 and A03) changes, as it
      must for any new sign-in method;
    - local lists are not backed up until the user signs in;
    - signing out leaves local screening running. UX A73 already says the
      same of carrier forwarding.
- **This ADR does not make that decision.** If the owner decides sign-in is
  always required, the table's server rows are unchanged.

### D32.6 Session and device-binding invariants: unchanged

- **Every issuance endpoint added here:**
  - requires a DPoP proof;
  - binds the session to its thumbprint;
  - issues only the ADR-0029 token profile;
  - accepts an optional ADR-0031 integrity token bound to the proof.
- **Nothing changes in:**
  - refresh rotation and reuse detection;
  - the 24-hour session;
  - edge-api's local verification.
- **ADR-0010 §10's controls are written for SIM swap.** Their analogue for a
  compromised mailbox or Google account is open decision 11:
  - notice to the previously enrolled device on a new enrolment;
  - a cooling-off period before number-dependent changes;
  - re-verification for elevated-risk operations.

**Recovery, compromise and revocation** (added in review):
- **Recovery is signing in again.** A holder who loses their device signs in
  on a new one with the same method, gets a new device-bound session, and
  keeps the same subscriber id (D32.1). Nothing on the old device transfers:
  its key never leaves it.
- **A lost method is a lost account,** unless another method is linked:
  - a Google-only holder who loses the Google account cannot recover;
  - an email holder who loses the mailbox cannot recover;
  - support cannot recover it either. Under design A (D32.2) it holds no
    address, and it must not move an account on a claim alone.
  - Linking a second method (D32.4) and, later, a verified number are the
    only recovery paths. A support-led recovery process is **not proposed**.
- **A compromised mailbox or Google account is a full account takeover**
  for everything that needs only an account (D32.5a), as SIM swap is under
  SMS. Number-dependent features keep their own gate (D32.5, P6).
- **Today's revocation is one session at a time.** identity has
  `RevokeSession` and erasure, and **no "sign out every device"**
  (`internal/app/identity.go`, `internal/pgstore/pgstore.go`). A new sign-in
  does not revoke the account's other sessions.
  - Proposed for open decision 11: a "sign out everywhere" that revokes
    every session family of the account, offered from any signed-in device
    and after a suspicious new enrolment.
  - Even then, access tokens already issued stay valid for up to 15 minutes
    (D9/O17).
- **Removing a linked method** (D32.4) should revoke either the sessions that
  method created or all sessions. Which one is part of open decision 4.
- **We hold no Google credential to revoke.** Only the ID token is exchanged,
  once. No Google access or refresh token is requested or kept.

### D32.7 Account deletion and erasure

- **In-app deletion is unchanged.** It is method-agnostic: refresh token plus
  DPoP proof, and the device key is destroyed after a confirmed deletion.
- **Erasure is extended, in the same transaction,** to delete:
  - every identifier row of the account (email, Google, verified number);
  - the encrypted address, if design B is chosen (D32.2);
  - email-code challenges, and their rate-limit and lockout state.
- **The random subscriber id is never reused** after erasure (INV-ID-7). It
  is random, so it carries no personal data.
- **The email suppression list** (D32.8) is keyed by the address pseudonym,
  not the account. Whether a suppression entry survives erasure (it protects
  deliverability, and records only that an address bounced or complained)
  is a **legal question (L2)**.
- **Unchanged:**
  - the erasure record and its retention (L2);
  - the audit log (L3);
  - the access-token residual window (D9/O17);
  - whether deletion needs a fresh re-authentication (D4/O12).
- **Re-registration after erasure** (D1/O9) now also covers email and Google
  identifiers. Tombstoning them (INV-ID-7) would mean keeping their
  pseudonyms, which is personal data. That is a legal question (L2).
- **Web deletion** (Play's requirement):
  - **For email accounts:** a deletion code by email, with a separate
    template and purpose, mirroring #193.
  - **For Google-only accounts:** Google sign-in on the web page. This needs
    a web OAuth client and the frontend stack (OD-8).
  - Both stay under D5/O13. **Sending a deletion code to the address inside a
    Google token is not proposed,** because Google may not be authoritative
    for that address.

### D32.8 Email provider, domain and deliverability: requirements only

**Provider:** none chosen.
- **The candidate consistent with ADR-0008** is Amazon SES in `ap-south-1`.
  AWS lists a Mumbai SES endpoint.

**How identity would authenticate to SES** (corrected in review: the first
draft said "no static credential" without naming the mechanism):

| Mechanism | What it is | Allowed? |
|---|---|---|
| **The SES API (v2 `SendEmail`), signed with SigV4 using the workload's IRSA role** | The pod's projected Kubernetes service-account token is exchanged by the AWS SDK with STS (`AssumeRoleWithWebIdentity`) for **temporary** credentials, refreshed automatically. Nothing long-lived is stored or mounted. | **Proposed.** It is what ADR-0008 means by "IRSA for every workload. No static cloud credentials anywhere" (`0008-cloud-infrastructure.md:73`, `:180`). |
| **The SES SMTP interface** | SMTP credentials are an IAM **user's** access key id plus a password **derived from that user's long-lived secret access key**, and they are per region. AWS states that SMTP credentials cannot be derived from temporary credentials. | **Rejected.** It needs a static IAM-user key, which ADR-0008 forbids. |
| **An IAM user's access key** in a Secret, an environment variable or a file | A long-lived static cloud credential | **Rejected** by ADR-0008. |

- **So "credential-free" is not accurate. The precise claim:** identity
  holds **no long-lived** AWS credential. It authenticates with short-lived
  STS credentials obtained from its IRSA role, through the SDK's
  web-identity provider.
- **The role's permissions** (SRE to define; nothing is created):
  - `ses:SendEmail` only;
  - restricted by condition to the verified sending identity, and, if SRE
    confirms the condition keys, to the `From` address;
  - no IAM, no identity management, no suppression-list administration.
- **Bounce and complaint events** (below) reach identity through an SES
  event destination, such as SNS to SQS. Reading them uses the same IRSA
  mechanism, with its own least-privilege permission.
- **New dependency:** identity has no AWS SDK today. The telephony gateway
  already uses `aws-sdk-go-v2` (RDS IAM). Adding the SES client to identity
  is a dependency change for review.
- **Egress:** identity reaches the regional SES API and STS endpoints.
  Whether VPC endpoints exist for both is for SRE to confirm.
- **The sandbox:** a new SES account is in the sandbox **per region**. It
  sends only to verified addresses, at most 200 messages a day and 1 a
  second, until production access is granted on request. That request is an
  external step, and is not taken.

**Any other provider:**
- a residency and data-processing assessment;
- its API key is a **long-lived third-party secret,** not a cloud
  credential. It is delivered as a mounted Secret file, read once at
  startup, never an environment value or a log line, as the SMS provider
  contract requires (`docs/runbooks/identity-provider-onboarding.md` §1a).
  Whether ADR-0008's rule extends to such keys is for the architecture team.

**Residency and privacy (ADR-0012, DPDP):**
- The address and the code pass through the provider and the recipient's
  mailbox provider.
  - SES in `ap-south-1` keeps **our processor** in India (ADR-0012 C4).
  - The recipient's mailbox provider, such as Gmail, is the person's own
    choice and may be outside India. We cannot control it. Whether delivery
    to it is consistent with C4 is for privacy review, not assumed here.
- **Google sign-in:** the person's Google account is held by Google, outside
  our control and possibly outside India. We receive only the ID token,
  verify it in India, and keep only the `(iss, sub)` pseudonym. Privacy
  review covers the disclosure in the notice (L4).
- The identity store keeps only pseudonyms (and, under design B, the
  encrypted address).
- **Privacy review must cover:**
  - the processing location;
  - a data-processing agreement;
  - the privacy policy (L4);
  - the Data safety form (L5: email address collected);
  - the data inventory (`docs/phase19/ANDROID-DATA-INVENTORY.md` lists none
    today).

**Sending domain:**
- A dedicated subdomain of the product domain. The domain itself is still
  undecided (B3).
- A custom MAIL FROM, so SPF aligns.

**Authentication of the sending domain:**
- **SPF and DKIM** (2048-bit keys), aligned with the `From` domain.
- **DMARC** published from day one, starting at `p=none` with aggregate
  reporting, then tightening to `quarantine` and `reject` once reports are
  clean.
- **Gmail's sender guidelines** require SPF or DKIM for all senders, and SPF,
  DKIM and DMARC for senders above 5,000 messages a day to Gmail. *(Read from
  secondary summaries; confirm against Google's page before relying on the
  threshold.)*

**Bounces and complaints:**
- Consume the provider's bounce and complaint notifications into a
  suppression list keyed by the address pseudonym.
- Never send to a suppressed address.
- Answer a request for one with the same generic response as any other.
- Alert on bounce and complaint rates.

**Content:**
- Transactional only. No marketing, ever, from this domain.

**Cost:** unverified.

### D32.9 Android: what would change (not implemented)

- **A sign-in method screen:** email first, Google where available.
- **An email-entry and code screen,** reusing the phone-verification
  ViewModel patterns (`feature/onboarding/.../PhoneVerificationViewModel.kt`).
- **Phone verification is hidden until SMS exists,** and becomes a later,
  optional "verify your number" step that unlocks number-dependent features.
- **New dependencies:** `androidx.credentials` and Google's ID library.
- **A new non-secret build value:** the Google server client id, added to the
  release endpoint preflight (#194).
- **Unchanged:**
  - the Keystore session store (`core/datastore/.../SessionStore.kt`);
  - the DPoP key (`core/security/.../KeystoreProofKey.kt`);
  - the authenticator's refresh and deletion logic.
- **Separate gaps the audit found:**
  - no signed-in check at launch;
  - no reaction to a signed-out state;
  - no sign-out action.

  They are not part of this ADR, but the new methods would make them visible.

### D32.10 SMS remains deferred, not removed

The SMS sign-in and deletion code paths, the delivery seam and the runbook
stay in the repository as they are, dormant.
- **India DLT registration and the SMS provider** stay future work (E2).
- **Their role changes** from "the sign-in" to "the phone-ownership
  verification" (D32.5).
- **The SMS sign-in route must not be re-enabled as it is** while email and
  Google accounts exist: it would create a second, number-keyed account
  (D32.5, P4). Its validate step is redesigned when SMS returns.

### Consequences

**Positive**
- **Sign-in is possible without an SMS provider or DLT registration,** which
  are the current critical path to internal testing.
- **Play review gets a real sign-in path** (OD-1): a reviewer mailbox or a
  Google account, with no production bypass.
- **The account survives a number change**, and moving to a new phone no
  longer depends on SIM possession.
- **Device-bound sessions, refresh security and deletion are untouched.**

**Negative**
- **ADR-0010's identity primitive and two domain invariants would change.**
  That is the most expensive kind of change in this repository, and it needs
  the full approval chain.
- **Two new external dependencies:** an email provider with DNS and
  deliverability operations, and Google, with egress and an outage mode.
- **New personal data:** the email address and Google identity.
- **Duplicate accounts,** if linking is deferred.
- **A mailbox or Google-account compromise becomes a takeover path.** It needs
  its own §10-style controls (open decision 11).
- **Number-dependent features remain unavailable** until SMS or carrier
  verification exists. The calling product's core value still waits on phone
  ownership.
- **A holder with one method who loses it loses the account** (D32.6). No
  support-led recovery is proposed.
- **Under design A (D32.2), we cannot email the holder** about a new sign-in,
  a deletion or a breach. Only in-app and push channels remain, and push is
  not built.
- **ADR-0030 D30.3 needs generalising,** and the long-lived lookup keys need
  a retirement rule (D32.1).

**Neutral**
- The session, token and device-binding code paths are shared by every
  method.

### Confidence

**Low to medium.** The security design follows existing, tested patterns.
These parts are unresolved:
- the governance path (review trigger, invariant sign-off);
- the phone-ownership gate's mechanism (open decision 9);
- the email provider's residency, and the recipient mailbox's location;
- the verifier library ADR;
- the ADR-0030 generalisation and lookup-key rotation (D32.1);
- whether an address is retained (design A or B, D32.2).

### Revisit Trigger

- SMS or silent carrier verification becomes available. Re-examine whether
  phone ownership should become mandatory at sign-up.
- Linking produces support load above an agreed threshold.
- Email-code delivery success falls below an agreed rate.
- A regulator or Play policy requires a verified phone number at sign-up.

## Options in Detail

### Option 1: Keep ADR-0010 (SMS only)

- **Good:** No architecture change. Number ownership is proven at sign-up.
- **Bad:**
  - Nobody can sign in until a provider and DLT registration exist.
  - It contradicts the owner's product direction.

### Option 2: Email code primary, Google alternative, phone ownership separate (proposed)

- **Good:**
  - Reuses the challenge, rate-limit, delivery, session and deletion
    machinery.
  - No SMS dependency for sign-in.
  - Honest separation of what each proof proves.
- **Bad:** The identity-primitive change and its approvals; the new
  dependencies; possible duplicate accounts.

### Option 3: Google only

- **Good:** No email-provider operations.
- **Bad:**
  - Excludes devices without Google Play services and users without a Google
    account.
  - A single external dependency for all sign-ins.
  - It is not the owner's direction.

### Option 4: Magic link instead of a code

- **Good:** One tap for the user.
- **Bad:**
  - Needs a verified App Links domain, and the link can open on a different
    device from the one being bound.
  - It is easily lost to link scanners that pre-fetch URLs.
  - It does not reuse the code machinery. A code keeps the session bound to
    the device that asked.

### Option 5: Hosted identity provider (Firebase Authentication, Cognito)

- **Good:** Less code to operate.
- **Bad:**
  - ADR-0010's grounds still apply: residency, and fit with the device-bound
    session (`:161-167`).
  - identity would still have to issue the DPoP-bound session itself, so the
    provider would only add a second trust root.

### Option 6: Email and password

- **Bad:** Prohibited by INV-ID-10 (`docs/domain/10-identity.md:450`) and
  ADR-0010. **Not considered further.**

## Approvals and open decisions

**Nothing below is approved.** Each item needs to be recorded before the work
it gates starts.

**How these are approved** (`ARCHITECTURE_FREEZE.md` §6.1, adopted in #199):
"architecture team" in this document means the repository owner's recorded
decision. Each architecture decision below is high-risk, so it also needs an
independent human review before acceptance, and stays Proposed without one.
The architecture review packet is
`docs/adr/reviews/0032-architecture-review-packet.md`.

### Architecture decisions (owner decision plus independent human review)

1. **Whether the owner's product direction is a sufficient review trigger**
   for ADR-0010 (`ARCHITECTURE_FREEZE.md:142-143`).
2. **A superseding decision for ADR-0010 §5(a)** (the identity primitive), and
   an update of the freeze summary row (`ARCHITECTURE_FREEZE.md:115`) if
   accepted.
3. **Explicit invariant sign-off** (`ARCHITECTURE_FREEZE.md:148`):
   - INV-ID-1 (number becomes optional and verified);
   - INV-ID-2 (applies to verified numbers);
   - INV-ID-7 (extended to the new identifiers);
   - INV-ID-8 (residency: the address reaches the person's own mailbox
     provider, and the Google identity is held by Google; D32.8);
   - INV-ID-3, INV-ID-9 and INV-ID-10 (unchanged, recorded for
     completeness).
4. **The ID-token verifier library,** as its own ADR (ADR-0029 `:106-108`).
5. **The phone-ownership mechanism** (D32.5, open decision 9): a new claim
   (amends ADR-0029), a `roles` value (no claim-set change, but authorisation
   state in the token), or a lookup.
6. **The domain and UX specifications:**
   - the `Subscriber` aggregate in `docs/domain/10-identity.md`;
   - screens A02 and A03 in
     `docs/ux/android/06-screens-onboarding-auth-permissions.md`.
7. **ADR-0030 amendments** (D32.1):
   - D30.3 generalised from a number-keyed table to identifier rows of
     several kinds;
   - the key purpose of the email and Google lookup pseudonyms;
   - a retirement rule for a lookup key's previous version.
8. **The SES integration's security shape** (D32.8): the IRSA role and its
   permissions, the AWS SDK dependency in identity, and whether ADR-0008's
   rule extends to third-party API keys.
9. **The SMS route's redesign** before SMS returns (D32.5, P4), and the
   "sign out everywhere" mechanism (D32.6).

### Owner decisions

1. **Ratify this product direction** through the architecture process above.
2. **O2 (ADR-0030 D30.3):** random stored subscriber ids. **Prerequisite.**
3. **The permanent package name** (undecided), and the **Google Cloud
   project,** OAuth consent screen and client ids. The Android client id is
   tied to the package name. Nothing is created.
4. **Linking:** ship it in the first release, or accept duplicate accounts.
5. **Whether a recoverable address is kept** (D32.2): design A, the
   pseudonym only (proposed default), or design B, the pseudonym plus an
   address encrypted under a KMS key. A plaintext column is rejected.
6. **Code parameters:** the email code's lifetime (10 minutes proposed), its
   rate limits, and the global hourly send budget value.
7. **The email provider and region,** the product domain and sending
   subdomain, and the DMARC tightening schedule.
8. **Whether on-device screening requires sign-in at all** (D32.5a; the
   recommendation is no).
9. **How edge-api learns phone-ownership state** (with architecture
   decision 5).
10. **A number already verified on another account:** refuse (proposed), or
    a support-led transfer.
11. **Controls for a compromised mailbox or Google account,** mirroring
    ADR-0010 §10, building on O5.
12. **Existing account-deletion decisions extended to the new identifiers:**
    D1/O9 (re-registration), D4/O12 (step-up), D5/O13 (web deletion), D9/O17
    (token revocation), and OD-8 (frontend stack).
13. **OD-1:** the Play reviewer path, using the new methods with no bypass.
14. **OD-10:** Phase 19 completion criteria, which today assume SMS sign-in
    for internal testing.
15. **Account recovery:** accept that a holder who loses their only method
    loses the account (proposed, D32.6), or fund a support-led recovery
    process, which needs its own security design.

### Privacy and legal

- **L1:** DPDP review, including the email address and Google identity, and
  whether breach intimation (ADR-0012 C8) can be met without a stored
  address (design A).
- **L2 and L3:** retention of erasure records, identifier tombstones and the
  email suppression list after erasure.
- **L4:** the privacy policy, naming Google sign-in and the email provider.
- **L5:** the Data safety answers (email collected).
- **Residency and a data-processing agreement** for the email provider
  (ADR-0012), and delivery to mailbox providers outside India (C4).

### External setup (none started; costs unverified)

- **Email:** a provider account, a verified sending domain with SPF, DKIM and
  DMARC records, bounce and complaint notifications, and egress. For SES:
  an IRSA role, the event destination, and a production-access request to
  leave the sandbox.
- **Google:** a Cloud project, the OAuth consent screen, and Android and
  server (and later web) client ids. The Android client is tied to the package
  name and the Play App Signing certificate. Also egress to Google's keys.
- **SMS:** unchanged, deferred (E2).

## Approval record

Not started. ADR-0032 is Proposed (see Status).

| Field | Record |
|---|---|
| Decision | None |
| Classification | Proposed by the author: high-risk. Owner's classification: not yet recorded |
| Version | Not yet decided on |
| Review trigger | Not ruled on (architecture decision 1) |
| Owner decision | None |
| Independent human review | **Not available.** No reviewer has been identified |
| Invariant sign-offs | None (INV-ID-1, INV-ID-2, INV-ID-7, INV-ID-8 pending) |
| Other approvals | None (privacy and legal L1–L5, residency and DPA pending) |
| AI involvement | Drafted by Claude Code; AI-performed review on 2026-10-31; AI-prepared review packet. None of these is an independent human review |
| Does not | Accept, supersede or amend anything; authorise any implementation |
| Supersedes | Nothing |

## References

**Repository decisions:**
- **ADR-0010** (Accepted): `docs/adr/0010-authentication-and-device-trust.md`
  `:14`, `:65-69`, `:108-112`, `:161-167`, `:183-187`, `:207-217`, `:304`,
  `:312-320`.
- **ADR-0029** (Proposed): token profile `:49`; library revisit `:106-108`;
  no remote JWKS for first-party tokens `:129-132`; roles `:140-141`.
- **ADR-0030** (Proposed): D30.1 one key per purpose; D30.2 lazy pseudonym
  rotation (`:83-89`); D30.3 random subscriber ids (`:92-113`).
- **ADR-0008** (Accepted): IRSA, no static cloud credentials (`:73`, `:180`).
- **ADR-0012** (Accepted): C4 residency, C5 erasure, C8 breach notification.
- **ADR-0031** (Proposed).

**Freeze and domain:**
- `ARCHITECTURE_FREEZE.md:115,140-149`, and §6.1 (governance amendment G0,
  #199)
- `docs/domain/10-identity.md:18,72-75,441-450`

**Phase 19 documents:**
- `docs/phase19/ROADMAP-INVENTORY.md` (C7–C13, OD-1–OD-11, L1–L6, E2)
- `docs/phase19/ACCOUNT-DELETION.md` (D1–D9)
- `docs/phase19/SIM-SWAP-CONTROLS-REVIEW.md`
- `docs/phase19/ANDROID-DATA-INVENTORY.md`

**External** (read 2026-10-31; nothing was created or contacted):
- Google, Authenticate with a backend server:
  <https://developers.google.com/identity/sign-in/android/backend-auth>
- Google, Authenticate with a backend using ID tokens (One Tap):
  <https://developers.google.com/identity/one-tap/android/idtoken-auth>
- Google, Email sender guidelines FAQ:
  <https://support.google.com/a/answer/14229414>
- AWS, Amazon SES endpoints and quotas:
  <https://docs.aws.amazon.com/general/latest/gr/ses.html>
- AWS, Obtaining Amazon SES SMTP credentials (derived from an IAM user's
  secret access key; not from temporary credentials):
  <https://docs.aws.amazon.com/ses/latest/dg/smtp-credentials.html>
- AWS, Request production access (the per-region SES sandbox):
  <https://docs.aws.amazon.com/ses/latest/dg/request-production-access.html>
- Google, OpenID Connect (`sub`, `iss`, `azp`, `email`, `hd`):
  <https://developers.google.com/identity/openid-connect/openid-connect>
- **Supersedes:** nothing. **Would require a superseding decision for:**
  ADR-0010 §5(a), if accepted.
