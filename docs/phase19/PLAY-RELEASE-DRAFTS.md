# Google Play release drafts: Data safety, privacy policy, listing and console answers (2026-11-11)

> **DRAFTS for the owner and for legal and privacy review (L4, L5, E9). Not legal advice. Nothing has been submitted to Google Play.** Every statement below is taken from [`ANDROID-DATA-INVENTORY.md`](ANDROID-DATA-INVENTORY.md) (traced to code and the merged release manifest) or marked **UNVERIFIED** or **OWNER**. The product name follows the owner's choice, "Lily AI" (2026-11-05). **Its use in a listing waits for the naming review (F26) and trademark counsel.**

- **As of:** `main` `290cdd9`.
- **Which build these describe:** the internal-testing build **with the identity and edge-api hosts configured**. A build from `main` with defaults makes no network request at all (inventory §1).
- **Keeping it current:** a change to permissions, SDKs or data flows updates the inventory first, then this file. **The planned flows in inventory §5** (forwarding, AI speech and LLM, transcripts, SMS, FCM) **change every answer below**, and must be declared before they ship.

## 1. Data safety form: draft answers

| Play question | Draft answer | Evidence | Owner confirms |
|---|---|---|---|
| Does the app collect or share any of the required user data types? | **Yes, it collects;** see the rows below | Inventory §3 | Q2 |
| Is all collected data encrypted in transit? | **Yes.** The clients refuse non-HTTPS origins, and pinning applies once pins are set | Inventory §6 Q5; ADR-0010 | Q5 |
| Can users request that their data be deleted? | **Yes: in-app account deletion** (#188, #189). A **web deletion URL is also required by Play** and is not built (C1, E10) | `ACCOUNT-DELETION.md` | Q1; needs E10 before production |
| **Personal info → Phone number** | **Collected, not shared.** Purposes: *Account management*, *Fraud prevention, security and compliance*. Required for sign-in | D1. Identity stores only an HMAC pseudonym; OTP and rate rows expire within about 25 h | Q2 |
| **Device or other IDs** | **Collected, not shared.** The app-generated device-key thumbprint (not a hardware ID). Purposes: *Security*, *Account management* | D3, D4 | Q2 |
| Contacts | **Not collected.** `READ_CONTACTS` is requested only on the subscriber's tap; the app never reads the contacts provider | Inventory §2 | Q3 |
| Call logs | **Not collected** | No call-log permission (§1) | Q3 |
| Incoming callers' numbers | **Not collected:** processed **on the device**, in memory, never stored or sent | D7 | Q3 |
| Block and allow lists | **Not collected:** device only, excluded from backup, no sync | D8 | Q3 |
| SIM details | **Not collected:** displayed only | D9 | Q3 |
| Crash logs and diagnostics | **Not collected:** no SDK, and the release build logs nothing | D11 | Q3 |
| App activity, location, messages, audio, photos, financial or health data | **Not collected** | Permission set (§1); no SDK | Q3 |
| **Play Integrity token** | **UNVERIFIED** how Google expects it to be declared. Identity's verifier is dormant, so the token is not sent to Google by the backend | D5 | **Q4: check Google's documentation at submission** |
| "Shared" with third parties? | **None today.** The only recipients are the operator's own services. An SMS provider, once added, is a **service provider** (processing on the operator's behalf); confirm the classification | Inventory §6 Q2 | Q2 |

## 2. Privacy policy: draft text (for legal review, L4)

**Bracketed text is for the owner or counsel to complete.** The draft states only verified behaviour, and its scope is the current build.

> **Lily AI privacy policy** (draft, [date])
>
> **Who we are.** [Legal entity name, address]. Grievance Officer under India's Digital Personal Data Protection Act: [name, email, address].
>
> **What the app does on your phone.** When you give Lily AI the call-screening role, Android asks the app about each incoming call. The app checks the caller's number against the block and allow lists you keep on your phone. **The number is checked on your phone and is not stored or sent anywhere.** Your lists stay on your phone, are not backed up to the cloud, and are not synced.
>
> **Contacts.** The app asks for contacts access only if you choose to block saved contacts too. **It does not read your contacts**: Android uses the permission to pass calls from saved contacts to the screening check.
>
> **Your account.** To sign in, you enter your mobile number and a one-time code sent by SMS. **We store a keyed pseudonym of your number, not the number itself.** [When an SMS provider is used: name the provider and say that it receives your number and the code solely to deliver the message.] A key created on your phone (it never leaves the phone's secure hardware) proves that requests come from your device.
>
> **How long we keep it.** Sign-in codes and request limits expire within about 25 hours. Your session ends [24 hours after sign-in]. Security audit records are kept under legal hold as [legal basis and period from counsel].
>
> **Deleting your account.** Settings → Delete account removes your account and signs this phone out. [Web deletion URL, once available.]
>
> **What we do not do.** No advertising, no analytics or crash-reporting SDKs, no sale of data. The app writes nothing to the device log in release builds.
>
> **Security.** All connections use HTTPS. [Certificate pinning once enabled.]
>
> **Changes.** [How users are told.] **Before the app forwards calls to an AI assistant or records anything, this policy will be updated and your consent asked for first.**
>
> **Contact.** [Email.]

**Legal and privacy review must check:**
- the DPDP notice requirements and the Grievance Officer;
- the legal-hold wording (ADR-0010 §10);
- the retention periods (O6, O14, D2, D3);
- the processor terms of any SMS provider (E9, L1).

## 3. Store listing: draft (truthful to the current build)

- **Short description (80 characters at most):** "Block unwanted calls on your phone. Your lists never leave your device." (71 characters)
- **Full description:**

> Lily AI screens incoming calls on your phone. Add numbers to your block list and Lily AI rejects them before they ring; add numbers you trust to your allow list. Everything is checked on your phone, and your lists are not uploaded or backed up.
>
> • On-device call blocking with Android's call-screening role
> • Block and allow lists you control, with undo
> • Optional: also screen calls from saved contacts
> • No ads, no tracking SDKs

- **Languages:** do **not** list Hindi, Tamil, Telugu, Malayalam or Kannada yet. The release is *packaged* for six languages (`android/app/release-locales.txt`), but none is translated (`LOCALISATION.md`): the app shows English. Add a language to the listing only when its translation ships.
- **Do not claim, before release milestone M3 evidence:**
  - AI answering;
  - call forwarding;
  - fraud or spam detection;
  - transcripts;
  - "AI screening".
- **Why:** those features are not built in the release (inventory §5; device cases M3–M5). The listing must not get ahead of the app (A09b copy fix, #288).
- **Prerequisites for the listing:** the name (F26 and trademark counsel); screenshots from the release build; category (likely *Communication*; the owner decides); contact email; the privacy-policy URL.

## 4. Other Play Console answers (drafts; the owner completes them in the console)

| Item | Draft | Basis |
|---|---|---|
| Ads | **No ads** | No advertising SDK (inventory §1) |
| Target audience | **18+** proposed (account holders sign in with a mobile number) | **OWNER** |
| Content rating questionnaire | Utility or communication app: no violence, sexual content, gambling or user-generated content | **OWNER** answers the questionnaire |
| App access (review sign-in) | **Blocked:** sign-in is SMS-only, so a reviewer cannot sign in without a working path (OD-1, C9). Until then, internal testing only | Release assessment §8.1 |
| Permissions declaration | `READ_CONTACTS` and the call-screening role: **UNVERIFIED** whether Play requires a declaration form. Check Play's "Permissions and APIs that access sensitive information" policy at submission. The app has no SMS or Call Log permissions, which are the restricted groups | Release assessment §8.5 |
| Target API level | 36 | Release assessment (VERIFIED) |
| Delete-account URL | **Missing** (C1, E10) | Required before production |

## 5. What each Play track needs

| Track | Needs | Status |
|---|---|---|
| **Internal testing** | R3 (account, package name D2, `play-release` Environment D5 before secrets D6); a release run; testers' Google accounts. Data safety and a privacy policy may also be required even for internal testing: **UNVERIFIED** | Waiting for R3 and F26 |
| **Closed and open testing, production** | All of the above, plus: this file's answers confirmed and submitted (L5); the privacy policy reviewed and hosted (L4); the web deletion page (C1, E10); a reviewer sign-in path (OD-1, C9); an R1 SMS provider live; release milestone M2 evidence (`OD-10-DECISION-2026-11-09.md`) | Not started |
