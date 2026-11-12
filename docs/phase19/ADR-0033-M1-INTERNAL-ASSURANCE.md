# ADR-0033 and the SMS adapters: internal assurance for an M1 decision (rule 8a)

> **AI-performed internal security assurance, by Claude Code. NOT an independent security review** (rule 9). It is the evidence rule 8a (`ARCHITECTURE_FREEZE.md` §6.2) lets the owner use to decide ADR-0033, and the SMS adapters in #301, **for release milestone M1 only**. An independent human review stays mandatory before M2 (rule 8b). **The decision is the owner's** (rule 6). This document asks for it; it does not make it.

- **As of:** `main` `89b501b`, with draft PR #301 at its head (adapters plus startup selection).
- **Scope:** the OD-12 package's S1–S5 (`OD-12-OTP-REVIEW-PACKAGE.md` §2):
  - S1: ADR-0033;
  - S2: the OTP lifecycle;
  - S3: the delivery boundary and failover;
  - S4: the development mock (#270);
  - S5: the delivery-only adapters (#301).
- S6 (#271, not selected) is out of scope.

## 1. What was examined, and the evidence

| Item | Examined | Evidence (tests run in CI on every change; `-race` included) |
|---|---|---|
| **S1: ADR-0033** (identity generates, hashes and verifies; the provider only delivers) | The design, against what `main` does | #301 uses neither MSG91's OTP API nor Twilio Verify. The adapters receive only the number and identity's code, and send identity's template |
| **S2: the OTP lifecycle** | Generation, storage, expiry, single use, attempt limits, per-number rate limits, lockout, races, enumeration, input validation | `INTERNAL-SECURITY-ASSURANCE.md`: 24 threats mapped, 23 covered by tests, no defect. Added in #302: entropy and `crypto/rand`, constant-time comparison, expiry, live challenges, input refusal without spending the proof, enumeration uniformity, the leak sweep |
| **S3: the delivery boundary and failover** | `otpdelivery.Sender` | Existing `TestSender_*`: provider text never leaks; the caller's deadline ends failover. #301: failover from MSG91 (down) to Twilio, observed outcomes |
| **S4: the development mock** | Selection rules | `TestResolveOtpDelivery_MockIsRefusedOutsideExplicitDevelopment`. #301 also refuses mock combined with a real provider, and a mock outbox with a real provider |
| **S5: the delivery-only adapters** (#301) | MSG91 and Twilio adapters; startup selection | Tests against local TLS stand-ins cover: request shape; **`+91` mobiles only, refused before any request**; six-digit codes; templates holding `{code}` once; credentials from absolute-path files; TLS to the provider host only; **no redirects**; fixed errors that never echo the number, code, key or provider text; context deadlines; failover; startup refusing partial or conflicting configuration; credential files read at startup. **Mutation-checked:** the India rule, redirect refusal, the stray-variable refusal |

## 2. Findings

**No defect was found.** The following matter for M1. Each one is the owner's decision, and none is fixed silently.

| # | Finding | Severity for M1 | Options |
|---|---|---|---|
| **F-1** | **SMS pumping (cost abuse).** Once staging is reachable and a real provider is configured, anyone can call `POST /v1/otp/request` for any `+91` mobile number. Only per-number limits exist (3 requests per 15 minutes per number), so **many numbers means many paid SMS.** The adapters bound it to the domestic route only (no premium or international numbers) | **High before activation.** None while no provider is configured | (a) **ADR-0033 Q3: a global sign-in send budget** per hour (as deletion codes already have); (b) **an M1 tester allowlist**: only listed numbers receive SMS during internal testing; (c) per-source rate limiting (C13). **Recommended: (b) for M1, plus (a).** Both are identity changes, each made after your decision |
| F-2 | **Sign-in accepts any E.164 number,** although the adapters refuse non-Indian ones (the request then fails with "delivery unavailable") | Low: no message is sent | ADR-0033 Q2: refuse non-`+91` numbers at request time, giving a clearer error and no challenge row |
| F-3 | **Earlier live challenges stay valid** after a new request. They share the number's 5-failure budget | Low | Q4 |
| F-4 | **No resend cooldown** apart from the per-number limit | Low | Q5 |
| F-5 | **The wire formats are not verified against the live providers** (ADR-0033 Q9) | Medium: the first real send may fail | The onboarding runbook's cases S1–S6 on a real phone, after R1 |

## 3. Decisions requested (owner)

1. **Accept ADR-0033 for M1 under rule 8a.** It would be recorded as "accepted for M1 on internal assurance; independent human review outstanding". An independent review is required before M2.
2. **Authorise #301 to merge for M1.** It brings the delivery-only adapters and their startup selection. **Merging activates nothing:** without R1 and its variables, identity behaves exactly as today.
3. **Decided 2026-11-12:** both controls were approved, and built in #301 (`0a8e88d`). Startup refuses a real provider without both. Mocks and local fakes only; no real SMS was validated. **Original text:** **Choose the F-1 control before any real SMS:** a tester allowlist for M1, a global send budget, or both (recommended), with the values (the testers' numbers; an hourly cap). Claude Code implements and tests the choice as an identity change under the same rule.
4. **Optional for M1:** Q2 (India-only at sign-in), Q4 and Q5.

**What none of this authorises:** activating a provider, paid SMS, DLT registration (R1), deploying staging (R2), or production (M2).
