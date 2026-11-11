# OD-12: reviewer shortlist, recommendation and engagement plan (2026-11-11)

> **Superseded 2026-11-11 by the owner's revised direction:** no external security firm is to be engaged, and the hiring and quotation process is stopped. **Nothing was sent to any candidate.** This document is kept as a record only. OD-12 stays open; see [`OD-12-REVISED-DIRECTION-2026-11-11.md`](OD-12-REVISED-DIRECTION-2026-11-11.md). The archive tool (§6) remains useful under option A there.

- **As of:** `main` `ea751f8`. Research done on 2026-11-10 and 11 from **public sources only**: firms' own pages, their published reports, the CERT-In list and public price data.
- **Nobody was contacted.** No form was submitted, no quote requested, no account created, and no project detail was used in any search.
- **Status: a recommendation.** OD-12 stays **open** until the owner engages a reviewer and receives the report (rules 8 and 10). Claude Code cannot be the reviewer (rule 7).
- **Labels:**
  - **Verified:** checked by Claude Code against the primary document. The quotes are from it.
  - **Reported:** found by the research, not re-checked.
  - **Unverified:** no public source found.
- **Where this sits:** it answers the owner's 2026-11-11 direction to run reviewer discovery. It extends [`OD-12-REVIEWER-ENGAGEMENT-BRIEF.md`](OD-12-REVIEWER-ENGAGEMENT-BRIEF.md) and [`OWNER-ACTION-PACK-2026-11-10.md`](OWNER-ACTION-PACK-2026-11-10.md) §2–§5. The scope it serves is [`OD-12-OTP-REVIEW-PACKAGE.md`](OD-12-OTP-REVIEW-PACKAGE.md) §2.

## 1. Recommendation

**Primary for Batch A: Cure53 (Berlin).**
- **Its published work is the closest match in both subject and size.** In May 2025, Cure53 ran a white-box penetration test and source code audit of the IVPN DNS app's UI and API. Verified, from the report:
  - "A total of six days were invested … a team of four senior testers was assigned".
  - The findings include "Insufficient rate-limit on 2FA TOTP verification endpoint (Low)" and "Weak TOTP Backup Code Generation (Medium)". The backup codes relied "on a predictable random" generator.
- **Those are the threat classes at the centre of Batch A:** code generation and randomness, attempt and rate limits, and lockout.
- **Its other published work covers adjacent ground** (reported): Passbolt SSO and its missing rate limit on 2FA codes (2023), the authentik identity provider (2023), and several Android app audits. That is useful for Batch B.
- **Gaps to close at engagement:**
  - **No published Go audit was found.** Ask that the assigned testers have Go experience, and name them.
  - **A formal retest is not described** on their site. Ask for a fix review in the quote.
- **Why it ranks above the alternates:** the only public evidence of a job this size on exactly this subject. A 3–5 reviewer-day scope fits how it reports its work.

**Alternate 1: Trail of Bits (US). The strongest technical evidence, the least certain fit in size and cost.**
- **Verified, from its Wikimedia OATHAuth review (report of August 14, 2026):**
  - "A team of two consultants conducted the review from March 23 to April 3, 2026, for a total of four engineer-weeks".
  - The findings include "TOTP rate limiting and replay protection fail open" (Medium) and "Recovery code verification and consumption are not atomic".
  - The report is the "Final Comprehensive Report with Fix Review".
- **Verified, from the Go blog (19 May 2025):** Trail of Bits audited Go's core cryptography packages, including "authentication (HMAC), as well as the cryptographic random number generator". "Three Trail of Bits engineers worked on the audit for a month."
- **The risk:** its comparable job was about four times Batch A's estimate. Whether it takes a 3–5 day engagement, and at what price, is **unverified**.
- **Best placed for Batch B,** with its Go crypto and authentication depth and its standard fix review.

**Alternate 2, based in India: Payatu (Pune).**
- **Verified:**
  - **It is on CERT-In's list of empanelled auditors** ("146. M/s Payatu Security Consulting Private Limited"; the list's file is dated 2026-11-06).
  - **Its code-review page lists Go,** and its process ends with a "Re-review: After you fix, we check the changes and confirm the flaw is truly closed".
- **Reported:** Android depth (the DIVA-android training app; Frida, Objection and Drozer in its toolset); its founders run the nullcon conference.
- **Gaps:**
  - **No public OTP-specific work** was found.
  - **Its own CERT-In skills snapshot (2024) lists only 3 source code reviews** in 12 months.
- **The case for it:** lower cost, India's working hours, and familiarity with India's context (DLT, DPDP) is likely but **unverified**.

**Not recommended for Batch A, but kept on the list:**
- **NCC Group:** the strongest Go evidence. Its x/crypto/ssh review was 25 person-days with a retest (reported). Its interest in a small job is unknown.
- **Doyensec:** found a 2FA bypass across an API and an Android app (HEY, 2020; reported); has OAuth and session research. A Batch B candidate.
- **Appsecco:** the only firm with **published prices** (verified: "Small $5,000–$7,500 … 3–5 business days"; "one free re-test within 30 days"). But its offer is penetration testing, not source code review, and it is not on the CERT-In list.
- **SISA:** on the CERT-In list (verified #174), CREST-listed and ASVS-mapped code review (reported). No Go evidence.

**Independence:**
- No public partnership with Twilio, MSG91, Exotel, Gupshup, Kaleyra, Infobip, Plivo or Vonage was found for any candidate.
- These were generic searches, so they prove nothing. **The signed conflict-of-interest declaration** (brief §4) is the control.

**CERT-In empanelment is not a requirement here.** No record in this repository requires it, and rule 8 needs an *independent human reviewer*. Empanelment matters for government audits. It is listed as information only.

## 2. Shortlist (evidence and sources)

| Rank | Candidate | Relevant evidence | Go | Process | Fit for 3–5 days | Sources |
|---|---|---|---|---|---|---|
| 1 | **Cure53** (Berlin) | **Verified:** IVPN, 6 days, white-box, TOTP rate limit plus weak TOTP backup codes (May 2025). Reported: Passbolt SSO 2FA rate limit; authentik; Android apps | Unverified | Email (PGP or S/MIME); testers in contact with the developers during the test; retest not described | **Strong:** published 6-day job | [IVP-08](https://www.ivpn.net/resources/IVP-08-report.pdf), [Passbolt SSO](https://cure53.de/pentest-report_passbolt-sso.pdf), [authentik](https://cure53.de/pentest-report_authentik.pdf), [cure53.de](https://cure53.de/) |
| 2 | **Trail of Bits** (US) | **Verified:** OATHAuth 2FA, 2 consultants, 4 engineer-weeks, fail-open TOTP rate limiting, non-atomic recovery codes, fix review | **Verified:** Go crypto audit (HMAC, RNG) | Kickoff, status meetings, draft, readout, final with fix review (verified timeline) | Unverified; comparable job ~4× larger | [OATHAuth](https://github.com/trailofbits/publications/blob/master/reviews/2026-08-wikimediafoundation-oathauthmediawikimodule-securityreview.pdf), [Go blog](https://go.dev/blog/tob-crypto-audit), [publications](https://github.com/trailofbits/publications) |
| 3 | **Payatu** (Pune) | **Verified:** CERT-In #146. Reported: DIVA-android; advisories; nullcon | **Verified:** listed | **Verified:** ends in a re-review of fixes | Unverified | [code review](https://payatu.ai/code-review-service/), [CERT-In list](https://www.cert-in.org.in/PDF/Empanel_org.pdf), [advisories](https://payatu.com/advisory/) |
| 4 | **NCC Group** (UK, global) | Reported: Go x/crypto/ssh, 25 person-days, multi-factor permission finding, retest (2026) | Reported: strong | Retest stated in the report | Unverified | [x/crypto/ssh](https://www.nccgroup.com/media/lu1jlfli/ncc_group_go_-xcryptossh-cryptographic_report_.pdf), [go-cose](https://research.nccgroup.com/2022/05/26/public-report-go-cose-security-assessment/) |
| 5 | **Doyensec** (US, EU) | Reported: HEY API plus Android, 2FA bypass (2020); OAuth cheat sheet | Reported: safeurl (Go library), Go advisories | "A small client base" | Unverified | [HEY](https://www.hey.com/security/external-audits/doyensec-Q3-2020.pdf), [research](https://doyensec.com/research.html), [OAuth](https://doyensec.com/resources/Doyensec_OAuth_CheatSheet.pdf) |
| 6 | **Appsecco** | Reported: authentication, session and rate-limit testing; founder's Black Hat and OWASP record. **Verified:** prices | Not found | **Verified:** scoping call; fixed price; 50% at start and 50% on delivery; one free retest within 30 days | **Verified:** "Small … 3–5 business days" | [pricing](https://appsecco.com/pricing), [about](https://appsecco.com/about) |
| 7 | **SISA** (Bengaluru) | **Verified:** CERT-In #174. Reported: CREST; code review mapped to ASVS; optional follow-up review | Not found | Reported: scoping, review, walkthrough | Unverified | [code review](https://sisa.ai/solution/security/security-testing/application-security/secure-code-review-manual-automated), [CREST](https://crest-approved.org/member_companies/sisa) |

**Considered and set aside** (reported evidence only):
- 7ASecurity: every published job was 26 working days or more.
- X41 D-Sec and Radically Open Security: no OTP or authentication evidence found.
- Securing and Include Security: no published client audits found.
- SecureLayer7: it claims CERT-In empanelment, but it is not on the 2026-11-06 list.
- Varutra: acquired.
- Kratikal, Network Intelligence, QRC Assurance, Briskinfosec, Aujas and AKS: compliance-led, with no Go or OTP evidence found.
- Atredis and Bishop Fox were only briefly searched, so leaving them out is **not** a judgement on them.

## 3. Batch A and Batch B compared

| | **Batch A: the OTP review** | **Batch B: other authentication decisions** |
|---|---|---|
| **Scope** | S1–S7 in the package §2: ADR-0033; the OTP lifecycle (generation, hashing, expiry, attempts, single use, rate limits, lockout); the delivery boundary and failover; the development mock (#270, post-merge); a future delivery-only adapter (S5, before merge); sign-out (S7, if OD-14 is A or B); optionally the Play reviewer sign-in path (OD-1) | ADR-0029 (access token and DPoP), ADR-0030 D30.3 (stored subscriber ids), ADR-0031 (Play Integrity at issuance); session lifetime (O4); SIM-swap policies (O5); what a restricted session may do (O8); step-up before deletion (O12); token revocation (O17, H2); a refused refresh proof (H7) |
| **Decisions it unblocks** | ADR-0033 and Q2–Q10, O3, OD-14, OD-1, part of O13 | O1, O2, O4, O5, O7, O8, O12, O17, H7 |
| **Milestone** | **M1** (the SMS adapter C2 is on the critical path) | M2 (identity hardening; T4 stays unresolved until O7, E1 and OD-9) |
| **Code in scope** (non-blank lines, measured at `ea751f8`) | identity's OTP core: `identity.go` 2,026; `otpdelivery` 446 (tests 841); `bootstrap` 1,060. identity in all: 7,753 (tests 17,255) | `accesstoken` 601, `dpop` 438, `edge-api` 1,018, `playintegrity` 354, `pgstore` 1,308 (Go); Android `core/security` 399, `core/auth` 470, `core/datastore` 229, `core/network` 403 (Kotlin) |
| **Skills** | OTP and SMS threats (pumping, SIM swap, enumeration, lockout denial of service); Go; ASVS chapter 2; NIST SP 800-63B | JWT and OAuth profiles; DPoP (RFC 9449); Android Keystore and Play Integrity; session security; Go and Kotlin |
| **Effort** | **3–5 reviewer-days, plus about 1 for S7** (brief §5; Claude Code's estimate, **not a quote**) | **Not estimated.** On code size alone it is at least comparable to Batch A, plus an Android client. Ask the reviewer to scope it |
| **Best-fit candidates** | Cure53; then Trail of Bits; then Payatu | Trail of Bits; Doyensec; NCC Group |
| **Archive** | Ready: §6 (237 files) | Not built: it would add the Android modules. Built only when Batch B is approved |

**Recommendation:** engage Batch A now, which alone unblocks M1. Ask the chosen reviewer to quote Batch B separately, as an option.

## 4. Budget ceiling (a recommendation for the owner to approve or change)

**Public price data** (none of it a quote to this project):

| Source | Figure | Status |
|---|---|---|
| Appsecco pricing page | "Small $5,000–$7,500 … 3–5 business days"; "Medium $7,500–$12,500 … 5–7 business days"; retest included | **Verified** |
| UK public-sector rate cards (G-Cloud 14; Stingrai index, 2026) | Median £1,000 a day; range £480–1,600 | Reported |
| CCL Forensics, G-Cloud 15 "Secure Code Review" | £950 associate, £1,175 lead, £1,400 principal, per day | Reported |
| Syslifters (Austria) pricing | €1,600 a person-day | Reported |
| Secure Ideas (US) | US$340 an hour | Reported |
| Cyber Secify (Bengaluru) pricing | ₹74,999 for 6 business days of a single scope; one retest free; 50% in advance | Reported |
| Indian government (NICSI, 2026) | ₹10.8–16.2 lakh per critical government application (full audit with source code review) | Reported; far larger than this scope |

**Derivation:** Batch A at its upper estimate (5 days plus 1 for S7), plus 2 retest days, is **8 reviewer-days**.
- **International specialist:**
  - at the highest reported day rate, 8 × US$2,550 (US$340 × 7.5 h) = **US$20,400**;
  - at the UK principal rate, 8 × £1,400 = **£11,200**.
- **India-based firm:** the reported Indian market range is ₹40,000–1.5 lakh per web application and ₹1.5–4 lakh for a combined programme (TCSA, 2026; reported).

**Recommended ceilings for Batch A, retest included, before taxes:**
- **International: US$20,000.** That is about 8 reviewer-days at the top of the reported rates, and above Appsecco's verified "Medium" band.
- **India-based: ₹4,00,000.** The top of the reported Indian market band. It leaves room for a senior manual code review, which is costlier than the scanner-led tests most Indian price pages describe.
- **Taxes:** GST in India, or GST under reverse charge on a foreign supplier. **Have an accountant confirm.**
- **Payment:** at most 50% in advance. That is Appsecco's verified term and common in the reported data.
- **The statement of work states the ceiling as a maximum,** billed on work done.
- **Batch B is outside this ceiling.** It gets its own quote and its own approval.

## 5. Engagement package (what is ready)

| Item | Where | State |
|---|---|---|
| The decision under review and the package | ADR-0033; [`OD-12-OTP-REVIEW-PACKAGE.md`](OD-12-OTP-REVIEW-PACKAGE.md) | Ready; the code in scope is unchanged since it was written |
| Selection checklist; conflict-of-interest declaration | Pack §3; brief §4 | Ready |
| First-contact message | Pack §4, with one change: **in the first message, leave out the product name and any repository detail**, and describe it as "an Android call-screening app with a Go backend". Names and code follow the confidentiality agreement | Ready; **not sent** |
| Confidentiality terms to require | Pack §3: use only for the review; no model training; return or delete the materials at the end; the report confidential to the owner; responsible handling of findings | Ready as a term list. **The agreement itself is the owner's** (or counsel's) |
| The source archive | §6 | Built and verified locally; **not shared** |
| The deliverable | Package §9: per-item statements; findings with severity, file, line, evidence and fix; answers to §7; a recommendation on ADR-0033 | Ready |

## 6. Fixed-commit source archive plan

**Tool:** `tools/scripts/review-archive.sh <commit> <out-dir> [--verify]`. It:
- **refuses any commit not on `origin/main`.** Checked: the owner's local branch head `ad4a734` is refused, so local branches and uncommitted work can never reach a reviewer through it;
- archives a **fixed allowlist** with `git archive`:
  - identity and edge-api;
  - the ten shared packages they build against;
  - the real-PostgreSQL evidence script and its workflow;
  - ADR-0010, ADR-0033, the review package, `OTP-PROVIDERS.md`, the provider-onboarding runbook and `SECURITY.md`.
  - **Not:** the Android app, telephony, voice, infrastructure, other services, or any other document;
- **checks the Go modules are closed:** every local `replace` resolves inside the archive. Checked: removing one package is reported, naming both modules that need it;
- writes `MANIFEST.txt` (commit, date, scope, paths, file count) and `SHA256SUMS`. The bytes are **reproducible**: two builds at `ea751f8` gave the same SHA-256 (`ac318e21…fd1418`);
- with `--verify`, extracts the archive and runs `go build`, `go vet` and `go test -short` on identity and edge-api **from the archive alone**. Checked at `ea751f8`: 237 files; both modules build, vet and pass.

**Procedure, once the owner approves sharing:**
1. **The owner names the commit.** Claude Code confirms that CI on it is green, the code in scope is unchanged since the package (or updates the package), and `identity-postgres` passed.
2. Claude Code builds the archive with `--verify`, and records the commit and SHA-256 in OD-12's record.
3. **After the confidentiality agreement is signed,** the owner sends the archive through an encrypted channel the owner chooses. The SHA-256 goes separately, so the reviewer can check it. **No repository access is granted.**
4. The reviewer reports against that commit. A fix review is run against a later commit, with a new archive and checksum.
5. At the end, the reviewer confirms in writing that the materials were deleted.

**Nothing in the archive is secret.** It holds no credential, key or personal data: no PEM private key appears in the modules in scope (checked), and gitleaks passes on `main` in CI. It is still unpublished source code, so it is shared only under the agreement.

## 7. What the owner is asked to approve

These are the first external commitments. Nothing happens until they are approved.

1. **Contact:** approve sending the first-contact message (§5: no product name, no code) to **Cure53, Trail of Bits and Payatu**, together, asking each for:
   - availability;
   - a fixed-fee quote for Batch A with a retest, and optionally Batch B;
   - the reviewers' Go experience;
   - the signed conflict-of-interest declaration.
   - **Who sends:** the owner, or Claude Code from the owner's mailbox if the owner authorises it explicitly.
2. **The budget ceiling for Batch A:** US$20,000 (international) or ₹4,00,000 (India-based), retest included, before taxes. A quote above it comes back to the owner.
3. **How code is shared:** the fixed-commit archive (§6) after a signed confidentiality agreement. No repository access.

**Later approvals** (each separate, after the quotes arrive):
- choosing the reviewer;
- signing the agreement and the engagement;
- paying;
- sharing the archive.

**Claude Code will then:**
- compare the replies against pack §3;
- recommend one reviewer;
- prepare the engagement terms;
- build the archive at the named commit.
