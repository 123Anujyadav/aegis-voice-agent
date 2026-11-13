# Authentication decisions for the owner: Google Sign-In and passkeys (2026-11-13)

- **What this is:** the eight owner decisions that ADR-0035 (Google Sign-In and passkeys; Proposed) and its library ADRs, ADR-0036 and ADR-0037 (Proposed), need. For each: the exact wording proposed for the record, the options, the recommendation, the security implications, the user impact, and whether it blocks M1 or M2.
- **Decides nothing.** Every ADR here stays **Proposed** until the owner records a decision (`ARCHITECTURE_FREEZE.md` §6.1 rule 6; for M1, §6.2 rule 8a). Claude Code drafted it and accepts nothing (rule 7). An AI-performed analysis, not an independent review (rule 9).
- **About "blocks M1":** the M1 criterion is method-agnostic ("a tester signs in on a physical phone against staging"; OD-10). "Blocks M1" below means **blocks M1 through Google Sign-In and passkeys.** M1 through SMS (MSG91, after DLT and R1) stays possible and is not changed.
- **MSG91 is untouched by every option here.**

## Summary

| # | Decision | Recommendation | Blocks |
|---|---|---|---|
| 1 | Accept ADR-0035 for M1 (rule 8a), with ADR-0036 now and ADR-0037 before passkeys | Accept for M1 on internal assurance | M1 (this path); M2 needs the independent review |
| 2 | Invariant sign-offs, and O2 (random account ids) | Sign off as proposed; INV-ID-7 option A | M1 (this path) |
| 3 | ADR-0032's email-first direction | Withdraw it; ADR-0035 replaces it | Neither (clarity) |
| 4 | SMS as a sign-in route | Not offered while ADR-0035 is in force; SMS becomes phone verification later | Neither for M1; M2 scope |
| 5 | The M1 gate for new accounts | Google tester-email allowlist + hourly account-creation cap | M1 (this path) |
| 6 | Logout and revocation (OD-14) | Option B: server logout + revoke-all | M1 (sign-out); M2 |
| 7 | Recovery when every method is lost | No support process at M1; decide before M2 | M2 |
| 8 | External setup | Approve, in order: package name, Google Cloud project and OAuth clients, domain, staging | M1 (physical devices) |

---

## Decision 1: Accept ADR-0035 for M1 under rule 8a

**Proposed wording for the record:**
> "ADR-0035 (Google Sign-In and passkeys; phone number as a separate verification capability) is accepted for release milestone M1 on internal assurance; independent human review outstanding, and required before M2 (rule 8b). It supersedes ADR-0010 §5(a) for M1 only; the owner rules that the basis is the product direction of 2026-11-13 (no §16 trigger fired). ADR-0036 (go-oidc v3 and Android Credential Manager) is accepted with it; ADR-0037 (go-webauthn) is accepted before the passkey slice. This does not authorise any external setup, deployment or production use."

**Options:**
- **A.** Accept ADR-0035 and ADR-0036 now, ADR-0037 before passkeys (recommended).
- **B.** Accept ADR-0035 with amendments (name them).
- **C.** Accept Google Sign-In only (D35.1–D35.4, D35.6–D35.11); defer passkeys (D35.5, ADR-0037).
- **D.** Reject: keep SMS as the only sign-in (M1 through MSG91 after DLT and R1).

**Recommendation:** A. Google first is a complete, testable slice; passkeys follow on the same account model.

**Security implications:**
- Identity trusts Google's ID-token signatures for account creation and sign-in.
- Every credential is verified server-side.
- Sessions stay bound to the device's non-exportable key.
- No independent human review exists yet. Rule 8b keeps every change accepted here out of production until that review is done.

**User impact:** testers sign up with their Google account in a few taps; no SMS and no waiting on DLT.

**Blocks:** M1 through this path. M2 needs the independent review (OD-12).

## Decision 2: Domain invariant sign-offs, and O2 (stored random account ids)

**Proposed wording for the record (one line per invariant, rule 5):**
> "INV-ID-1 becomes 'A Subscriber has at most one active verified MSISDN'. INV-ID-2 becomes 'A verified MSISDN maps to at most one active Subscriber'. INV-ID-7 becomes 'An erased Subscriber's account id is tombstoned and never reused; its authentication identifiers and verified numbers are deleted' (option A). INV-ID-8 and INV-ID-10 are unchanged, with the rulings recorded in ADR-0035. INV-ID-3 and INV-ID-9 are unchanged. O2 (ADR-0030 D30.3) is accepted: new accounts receive stored random ids; existing number-derived ids are unchanged. All for M1 on internal assurance."

**Options:**
- **INV-ID-7 option A:** re-signup with the same Google account after erasure is allowed, as a new account. This matches today's code for numbers (O9).
- **INV-ID-7 option B:** keep the wording, so an erased Google identity can never sign up again. This needs a permanent list of tombstoned pseudonyms.
- **O2:** accept now, or keep number-derived ids, which would make Google- and passkey-only accounts impossible.

**Recommendation:**
- Sign off INV-ID-1, INV-ID-2, and INV-ID-7 option A.
- Record the INV-ID-8 and INV-ID-10 rulings.
- Accept O2.

**Security implications:**
- Accounts may have no phone number. Features that need a proven number must check the verified state.
- Option A lets an erased person return without a tombstone list. Option B keeps a permanent pseudonym list, which is more retained data.

**User impact:**
- Testers don't need to give a phone number to sign up.
- Option A: someone who deletes their account can create a new one later.

**Blocks:** M1 through this path. Without O2, a Google-only account cannot exist.

## Decision 3: ADR-0032's email-first direction

**Proposed wording for the record:**
> "The owner withdraws the 2026-10-31 email-first direction recorded in ADR-0032. ADR-0032 stays Proposed as a record; ADR-0035 replaces it as the authentication direction. Its Google analysis (D32.1, D32.3–D32.7) is carried into ADR-0035."

**Options:**
- **A.** Withdraw ADR-0032's direction (recommended).
- **B.** Keep email codes as a later addition, through a future ADR on the same account model.
- **C.** Keep ADR-0032 as the direction and reject ADR-0035.

**Recommendation:** A. B stays possible later.

**Security implications:** none directly. It removes two competing records for the same decision.

**User impact:** none.

**Blocks:** neither. It is for clarity of the record.

## Decision 4: SMS as a sign-in route

**Proposed wording for the record:**
> "While ADR-0035 is in force, the app does not offer SMS one-time codes as a sign-in. The existing OTP routes, the MSG91 adapter and every SMS control stay unchanged in code. A future ADR will define attaching a verified phone number to an existing account through the existing MSG91 path."

**Options:**
- **A.** Not offered as a sign-in; phone verification comes later (recommended).
- **B.** Offer SMS sign-in alongside Google. **Not recommended:** today's SMS route creates a separate account keyed by the number, so one person could end up with two accounts (ADR-0032 P4).
- **C.** Remove the OTP routes. **Not proposed:** the owner requires the MSG91 path to stay available.

**Recommendation:** A.

**Security implications:**
- A avoids duplicate accounts for one person, which would be a confusing state and could be used for account takeover.
- Nothing changes in the SMS code or its controls.

**User impact:**
- Testers sign in with Google (later, passkeys).
- Phone verification arrives later, for features that need a proven number.

**Blocks:** neither for M1. The phone-verification ADR is M2 or M3 scope (call forwarding needs a proven number).

## Decision 5: The M1 gate for new accounts

**Proposed wording for the record:**
> "At M1, a new account is created only for a Google identity whose verified email is on the owner's tester list (a mounted file, held as pseudonyms, used only for this gate and never stored), and at most N new accounts per clock hour across all replicas. With Google Sign-In enabled, startup refuses without both. Passkeys are registered only from accounts that passed the gate."

**Options:**
- **A.** Tester email allowlist plus an hourly account-creation cap (recommended; N = 5 per hour suggested).
- **B.** Invitation codes handed to testers. More moving parts, and the codes themselves become secrets.
- **C.** A Google-subject allowlist. The subject is unknown until the tester's first attempt, so it needs two steps.
- **D.** No gate. **Not recommended:** anyone with a Google account could create accounts on staging.

**Recommendation:** A.

**Security implications:**
- Bounds who can create accounts and how fast.
- The email is used only as a gate at M1. It is compared case-insensitively against `email_verified` true, and is never an identifier or linking key.
- Gate refusals give one generic answer.

**User impact:**
- Only the listed testers can sign up.
- Others see a generic "sign-in is not available" message.
- The owner supplies the 3–4 tester emails and N.

**Blocks:** M1 through this path. The gate must exist before staging accepts Google sign-ins.

## Decision 6: Logout and session revocation (OD-14)

**Proposed wording for the record:**
> "OD-14 is decided as option B: a sign-out action in the app that revokes the current session on the server (`POST /v1/session/logout`), and a 'sign out of all devices' action (`POST /v1/session/revoke-all`), both DPoP-bound. Access tokens may stay valid for up to 15 minutes after revocation (O17 remains open)."

**Options:**
- **A.** Local sign-out only, so the server session lives until its 24-hour expiry.
- **B.** A plus server logout and revoke-all (recommended).
- **C.** No sign-out.

**Recommendation:** B.

**Security implications:**
- A lost or shared phone can be cut off.
- Revoke-all limits a compromised Google account's reach.
- Residual: an issued access token stays valid for up to 15 minutes after revocation (O17).

**User impact:**
- Users can sign out and sign out everywhere.
- Credential Manager's state is cleared on sign-out.

**Blocks:** M1 for sign-out on testers' phones (ADR-0035's requirement); M2 in any case.

## Decision 7: Recovery when every method is lost

**Proposed wording for the record:**
> "At M1 there is no support-led recovery: an account whose every sign-in method is lost cannot be recovered. Users are encouraged to keep both Google Sign-In and a passkey. A recovery process is decided before M2."

**Options:**
- **A.** No support process at M1; decide before M2 (recommended).
- **B.** A support-led process now, verifying identity by other means. This is a social-engineering risk and needs a written procedure.
- **C.** A verified phone number as a recovery factor. This waits for the phone-verification ADR.

**Recommendation:** A.

**Security implications:**
- No new attack surface.
- Support-led recovery is the most common account-takeover route, and is deliberately not opened at M1.

**User impact:**
- A tester who loses their Google account and every passkey loses the Lily AI account; it holds only test data.
- Device replacement works through Google Sign-In or synced passkeys.

**Blocks:** M2 (production needs a defined recovery position).

## Decision 8: External setup approvals

Each item is an external action **only the owner takes or approves.** Nothing is created by this pull request.

**Proposed wording for the record:**
> "The owner will: (a) confirm the permanent package name after the naming review (D2/F26); (b) create a Google Cloud project with the OAuth consent screen, a server (web) client id and Android client ids for each package and signing certificate (debug, upload, Play App Signing), with no client secret placed in the app; (c) choose the HTTPS domain (B3) for identity and the passkey relying-party id, and host Digital Asset Links on it; (d) approve staging (R2). Each with its own approval of cost and scope."

**Options:** approve all four in that order (recommended); or approve (a) and (b) first and build against local fakes until (c) and (d).

**Recommendation:**
- Approve (a) then (b) first, so the Google slice can be built and tested against fakes.
- Then (c) and (d) for physical devices and passkeys.

**Security implications:**
- OAuth clients are bound to package name plus certificate, so a wrong package name means recreating them.
- No client secret goes into the Android app.
- Digital Asset Links bind passkeys to the app.
- Identity needs egress to Google's key endpoint, a network-policy change approved with staging.

**User impact:**
- None until done.
- Without (c) and (d), testers' phones cannot reach a backend, and passkeys cannot work at all.

**Blocks:** M1 on physical devices (both slices). Passkeys need (c) without exception.

---

## Dependency sequence

**Google Sign-In first:**
1. **Decisions 1–7** (owner).
2. **Decision 8 (a) and (b)** (owner): package name; Google Cloud project and OAuth clients.
3. **Backend:**
   - account and method tables, and the account directory (O2);
   - a separate session helper (OTP code untouched);
   - `POST /v1/auth/google` with the M1 gate;
   - tests against a local fake Google issuer.
4. **Logout and revoke-all** (decision 6).
5. **Android:**
   - Credential Manager behind an interface;
   - the "Continue with Google" screen and sign-out;
   - the client-id build value and its release preflight;
   - strings and translation worksheets;
   - the APK size baseline.
6. **Decision 8 (c) and (d):** domain and staging, then physical-device tests (sign-up, sign-in, restart, logout, revoke-all).

**Passkeys second:**

7. **ADR-0037** accepted; the relying-party id (the domain).
8. **`assetlinks.json`** hosted with the release and upload certificate fingerprints.
9. **Backend:**
   - challenge and passkey tables;
   - registration, authentication and deletion;
   - tests with a software authenticator.
10. **Android:** create a passkey (signed in), sign in with a passkey, delete one.
11. **Physical-device tests:** a second passkey, device replacement.

**Later (separate ADR):** attach a verified phone number through the existing MSG91 path, unchanged.

Each step is its own pull request in an isolated worktree, merged only on green CI. **No authentication code is merged before decision 1.**
