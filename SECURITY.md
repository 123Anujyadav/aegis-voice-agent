# Security Policy

## Reporting a vulnerability

**Do not open a public issue.**

Report privately to **security@callscreen.example** — replace with the real
address before the repository leaves the founding team — or through GitHub's
private vulnerability reporting on this repository.

Include: what you found, how to reproduce it, what an attacker could achieve,
and any suggested remediation. We acknowledge within **one business day** and
give an assessment with a remediation timeline within **five business days**.

We do not currently run a paid bounty programme. We do credit reporters in the
advisory unless you prefer otherwise.

---

## If a secret is leaked

**Rotate first. Remove from history second.**

This order is not negotiable and teams routinely get it backwards. Rewriting git
history takes time, and every minute spent on it is a minute the live credential
stays valid. A secret that reached a remote must be treated as compromised even
if the commit was force-pushed away — it existed in a fetchable ref, and clones,
CI caches and forks may retain it.

1. Rotate the credential at its source.
2. Revoke the old value and confirm the revocation took effect.
3. Check access logs for use of the old value.
4. Then purge it from history and notify the security owner.
5. Write a brief incident note in `docs/runbooks/`.

---

## What must never enter this repository

- Credentials, tokens, private keys or certificates in any form
- `.env` files with real values (`.env.example` only)
- Production connection strings, hostnames or internal network topology
- Customer data of any kind, including in test fixtures
- Call recordings or transcripts, real or derived from real calls

Enforced by `gitleaks` in pre-commit and CI, plus organisation-level push
protection. These are backstops, not permission to be careless: push protection
can be bypassed, and pre-commit hooks are not installed by everyone.

---

## Secret handling

Secrets live in the cloud secret manager, never in the repository and never in
an image.

| Layer            | Mechanism                                                  |
| ---------------- | ---------------------------------------------------------- |
| Source of truth  | Cloud secret manager, per environment, IAM-scoped           |
| Delivery to K8s  | External Secrets Operator → mounted **files**, not env vars |
| Workload identity| IRSA / Workload Identity — no static cloud credentials      |
| Repo-committed   | SOPS + age, for encrypted non-runtime config only           |
| CI/CD            | OIDC federation — zero long-lived secrets in GitHub         |
| Rotation         | ≤ 90 days; carrier and payment credentials ≤ 30 days        |

Secrets are mounted as files rather than environment variables because
environment variables leak into crash dumps, `/proc`, child processes, and any
library that logs its environment on startup. Files can also be rotated without
a restart.

### Android

**No API key, token or shared secret ever ships in an APK.**

An APK is a public artefact. Anything inside it — including strings in native
libraries and values obfuscated by R8 — is extractable in minutes by a motivated
attacker, and in seconds by an automated one. There is no such thing as a secret
in a mobile client.

The client authenticates by attesting itself with Play Integrity and exchanging
that attestation for a short-lived token from `identity`. If a design needs a
secret in the app, the design is wrong.

---

## Handling personal data

The product screens phone calls. It handles some of the most sensitive data a
person has: who calls them, when, and what was said. India's Digital Personal
Data Protection Act 2023 applies, and so does the plain expectation that a call
screening assistant does not leak your calls.

Two controls carry most of the weight:

**Schema-level classification.** Every field carrying personal data is annotated
in `contracts/proto/callscreen/common/v1/annotations.proto` with a sensitivity
class, a redaction strategy and a retention class. The platform libraries read
those annotations off the protobuf descriptor to drive log redaction, retention
and residency automatically. An unclassified field **fails closed** — it is
treated as personal.

**Redaction at the log sink.** `packages/go/platform` and
`packages/python/platform` enforce redaction inside the logging handler itself,
so there is no code path from a log call to a sink that bypasses it. Credentials
are dropped; personal identifiers are replaced with a keyed HMAC so records
remain correlatable without exposing the value.

Data classified as personal **must not leave the India region**. This is a
architectural constraint, not a configuration setting.

---

## Reporting a privacy incident

A privacy incident is any unauthorised access to, or disclosure of, personal
data — including personal data appearing in a log, a crash report, an analytics
event, or a support ticket.

Report it through the same channel as a vulnerability, and treat it with the
same urgency. Under DPDP, a personal data breach carries a notification
obligation to the Data Protection Board with a statutory clock. **The clock
starts when we become aware, not when we finish investigating**, so report
early and revise later rather than waiting for certainty.
