# OD-12: reviewer engagement brief (for the owner to send)

- **Prepared:** 2026-11-10, `main` at `bdde12c`, by Claude Code.
- **What this is:** a send-ready brief for the owner. It is used to approach candidate reviewers for the **independent human security review** that `ARCHITECTURE_FREEZE.md` §6.1 rule 8 requires for high-risk decisions (gate G0a, OD-12).
- **What this is not:**
  - **No reviewer has been contacted, selected or engaged.** Engaging one is a commitment: a contract, a payment, and sharing the repository. That is the owner's to make.
  - Claude Code cannot act as the reviewer (§6.1 rule 7), and its reviews never satisfy rule 8 (rule 9).
- **The detail behind it:** [`OD-12-OTP-REVIEW-PACKAGE.md`](OD-12-OTP-REVIEW-PACKAGE.md): the scope, threat analysis, evidence, commands (§3) and handoff (§9).

> **Note added 2026-11-10:** an updated message (current commit; Batch A now, Batch B quoted separately), the reviewer selection checklist and the review-readiness checklist are in [`OWNER-ACTION-PACK-2026-11-10.md`](OWNER-ACTION-PACK-2026-11-10.md) §3–§5. This brief's channels (§2), criteria (§3) and declaration (§4) still apply.

## 1. The message, ready to send

> **Subject:** Independent security review: OTP sign-in architecture of an Android call-screening app (Go backend)
>
> We are looking for an independent reviewer for a focused security review of the one-time-code (SMS OTP) sign-in design of an Android call-screening app, before any real SMS provider is connected.
>
> **Scope:**
> - one architecture decision (ADR-0033: the identity service generates, hashes and verifies codes; SMS providers only deliver them);
> - the existing OTP lifecycle in a Go service (generation, expiry, attempt limits, single use, per-number rate limits and lockout);
> - the provider-delivery boundary;
> - a development-only mock provider;
> - optionally, a sign-out change (S7).
>
> About 4,000 lines of Go across the files in scope (not all of it OTP code), with tests. Everything runs locally: Go 1.26.10 and Docker. No staging access or credentials are needed.
>
> **Deliverable:** a written report with severity-rated findings, a statement per scope item, and a recommendation (accept / amend / reject) on the decision.
>
> **We need from you:**
> - a short CV or firm profile showing authentication or OTP reviews;
> - a conflict-of-interest declaration (below);
> - your availability;
> - a fixed-fee quote, or a day rate with an estimate.

## 2. Where to look (legitimate channels; none contacted)

| Channel | Why it fits | Note |
|---|---|---|
| **CERT-In empanelled information-security auditing organisations** (the Indian Computer Emergency Response Team's published list) | An India-based product; the firms are vetted for security audits | Check the list's current edition on CERT-In's site. Firms vary in their application-security depth: ask for authentication and OTP references |
| **Independent application-security consultants** with public authentication work (OWASP ASVS chapter 2, mobile authentication), found through professional networks, OWASP chapter events (Bengaluru, Delhi and others), or published security write-ups | A focused review at lower cost than a full audit | Verify independence and references; agree confidentiality in writing |
| **Security consultancies offering source-code review** | Can also cover the later penetration test (E8) in one relationship | E8 is a separate owner decision with its own spend |
| **Academic security groups** (Indian universities with systems or security labs) | Independent by construction | Usually slower; confidentiality must be agreed |

## 3. Selection criteria (from the package §1)

**Required:**
- Not the drafter, and not anyone who wrote or directed the code: not Claude Code, not the owner.
- No commercial tie to any SMS provider under consideration (Twilio, MSG91 or others).
- Demonstrated review of authentication systems that use one-time codes. Familiarity with OWASP ASVS v4 chapter 2 and NIST SP 800-63B (out-of-band authenticators).
- Able to read and run Go tests.
- An understanding of SMS threats:
  - SIM swap and SS7 interception;
  - SMS pumping;
  - enumeration;
  - lockout denial of service.

**Helpful:**
- India's TRAI DLT rules;
- the DPDP Act's processor obligations;
- PostgreSQL concurrency.

## 4. Conflict-of-interest declaration (the candidate signs)

1. I did not write, direct or approve any of the code or decisions under review.
2. I have no financial or contractual relationship with Twilio, MSG91, or any SMS or OTP provider being considered, or I list it here: ______.
3. I have no relationship with the repository owner that would prevent an impartial report, or I list it here: ______.
4. I will report findings as found, including those that contradict the proposed decision.

## 5. Effort and cost

- **Scope S1–S6:** about 3 to 5 reviewer-days. S7 (sign-out), if added, is about 1 more. **This is an estimate**, not a quote.
- **Cost:** **unknown until quoted.** It is a paid engagement and needs the owner's approval of the spend, like any other external service.

## 6. What the owner must decide before sending

- **Repository access:** read-only access to the private repository, or an archive at a fixed commit. Either way, it is sharing source code with a third party, so a confidentiality agreement comes first.
- **The scope:** S1–S6 now, and S7 too if the sign-out option (`OWNER-DECISION-PACKET-2026-11-09.md` §4) is chosen.
- **The budget ceiling,** and who signs the engagement.

**After the review:** the report is linked from ADR-0033's approval record and from OD-12, and the owner decides ADR-0033 (package §8, §9). Until then, **OD-12 stays open and ADR-0033 stays Proposed.**
