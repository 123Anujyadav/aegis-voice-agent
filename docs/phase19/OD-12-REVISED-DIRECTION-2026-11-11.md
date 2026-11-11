# OD-12 under the revised direction: no external firm (2026-11-11)

- **As of:** `main` `879f312`.
- **The owner's direction (2026-11-11):**
  - do not hire or engage Cure53, Trail of Bits, Payatu or another external security firm;
  - stop the hiring and quotation process;
  - send nothing, and share no project information;
  - replace it with internal security assurance, kept distinct from independent assurance;
  - resume production engineering.
- **This record:** what was and was not done; OD-12's accurate status; and the owner decision the new direction needs, prepared explicitly.

## 1. The status of the enquiries: none was sent

- **Claude Code sent nothing.** No message, draft, form or quotation request went to Cure53, Trail of Bits, Payatu or anyone else. No email-sending or contact tool was ever used for this. The shortlist itself records "Nobody was contacted" (`OD-12-REVIEWER-SHORTLIST.md`).
- **The owner's mailbox could not be checked.** A read-only search of it was attempted on 2026-11-11, but the email connector needs the owner to sign in again. Whether the owner sent anything personally is therefore **unverified by Claude Code**. On the repository's records, nothing was sent.
- **The process is stopped:** no further research or contact. The shortlist is kept as a historical record, marked superseded.

## 2. OD-12's status: open, not waived

- OD-12 is "identify an independent human reviewer" (gate G0a; `ROADMAP-INVENTORY.md` §3.3). **The owner's direction not to hire a firm does not close it,** and it is not marked approved, complete or waived. It stays **open**.
- **The rules that make it matter are unchanged** (`ARCHITECTURE_FREEZE.md` §6.1):
  - **Rule 8:** a high-risk decision needs an independent human review before the owner accepts it.
  - **Rule 9:** an AI-performed review "never satisfies rule 8".
  - **Rule 10:** "No decision is accepted on the grounds that no reviewer was available."
- **What stays blocked while OD-12 is open:**
  - accepting ADR-0033 (OTP Option A);
  - merging any authentication change, including the delivery-only SMS adapter (C2) on M1's critical path;
  - the high-risk items O1, O2, O7, O9, O17, OD-2 and H1–H7;
  - every authentication-category decision in the register (`OWNER-ACTION-PACK-2026-11-10.md` §6).

## 3. The decision the new direction needs (OD-12R)

**Internal assurance is valuable evidence, but under the current rules it cannot unblock any of this.** Engineering can prepare the authentication work, but it cannot merge it. Either an independent human reviewer is still found by some other route, or the owner changes the rule. **Only the owner can do either** (rule 6).

| Option | What it means | Effect on M1 | Risk |
|---|---|---|---|
| **A. An independent human reviewer, not a firm** | A qualified person independent of the drafter: not Claude Code, not the owner, no tie to an SMS provider. For example, an engineer the owner knows. Rule 8 is met unchanged; the reviewer receives the archive (shortlist §6) under a confidentiality agreement | Unblocks it as soon as the review is delivered | Depends on finding the person. Still shares code (owner approval) |
| **B. Amend rule 8 for internal testing only** | The owner amends §6.1 (a recorded change, merged by the owner): for release milestone M1 (internal testing with known testers), the owner may accept a high-risk authentication decision on **internal assurance evidence** (§4), recorded as "accepted without independent review". **Independent review stays mandatory before release milestone M2** (production) and before any public release | Unblocks it without a reviewer | Internal-testing users are exposed to unreviewed authentication code. The risk is bounded by the tester count, the domestic route and the budget limits, but real phone numbers and real SMS are involved |
| **C. Amend rule 8 generally** | Independent review becomes optional for authentication | Unblocks everything | Removes the only human check on the highest-risk code. **Not recommended** |
| **D. No change** | Keep rule 8; no reviewer | **M1 cannot be met.** Authentication work stays as draft PRs | — |

**Recommendation for the owner to decide:** **B**, together with A **before M2**. Claude Code proposes it; it does not decide it (rule 7).
- B lets internal testing proceed on recorded, inspectable evidence.
- It keeps the independent check where real users begin.
- Under any option, **OD-12 stays open until an independent human review happens.** B only moves where it is required.

**If the owner chooses B,** the amendment text is ready to draft as a PR for the owner to merge. Claude Code cannot adopt it: rule 6 reserves changes to the rules to the owner.

## 4. The internal security assurance programme (what Claude Code does now)

Every artefact is labelled **"AI-performed internal assurance; not an independent security review."** The programme has five strands:

1. **Threat model:** the OD-12 package's §4, extended to sessions, tokens, DPoP and the API. Each threat is mapped to the test that covers it, or marked not covered.
2. **Code audit:** a line-level review of the OTP lifecycle, delivery, sessions and token checks, recorded as findings with file, line, severity and evidence.
3. **Automated security tests:**
   - OTP abuse: brute force, enumeration, lockout, races, input validation;
   - sessions: refresh rotation, reuse, expiry;
   - tokens and DPoP: algorithm, binding, replay;
   - the API: authentication on every route;
   - redaction: no number, code or token in logs or metrics.
   - **Test-only additions merge normally,** because they change no behaviour.
4. **Remediation evidence:** each finding gets a fix, a test that fails without the fix, and CI runs.
   - **A fix that changes authentication behaviour is itself an authentication change.** It stays a draft until OD-12R is decided.
   - **A fix outside authentication merges normally.**
5. **Evidence register:** CI run IDs, coverage reports (`identity-postgres` reports authentication-path coverage) and mutation checks.

**What it is not:**
- it is **not** independent assurance;
- it does **not** satisfy rule 8;
- it is **never** described as an audit or a review by a third party.

## 5. What this does not change

- **ADR-0033 stays Proposed.** The approved OTP architecture is preserved (identity generates, hashes and verifies codes; providers only deliver).
- **The SMS scope stays India-only, domestic, with no international fallback.** Q2 (enforcing it) stays an open owner decision.
- **The ratified Ollama endpoint policy** and every other owner decision are unchanged.
- **Milestones:** M0 only. **No production readiness is claimed.**
