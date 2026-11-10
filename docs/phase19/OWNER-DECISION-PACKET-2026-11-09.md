# Owner decision packet (2026-11-09): OD-18, OD-16, three Android UX choices, OD-14

- **Prepared:** 2026-11-09, `main` at `04d02c6`, by Claude Code at the owner's direction.
- **What this is:** four decision briefs. Each lists the facts from the repository, the options, and a recommendation.
- **Nothing here is decided or implemented.** No choice below is applied until the owner records it, and none is merged or changed in the meantime.
- **The analysis is AI-performed.** Where a statement depends on Android or GitHub behaviour this project has not measured, it says so.

| # | Decision | Recommendation | Classification |
|---|---|---|---|
| 1 | OD-18: the four held Dependabot PRs | Merge #183, #184 and #185 after a rebase onto `main`; merge #212 after the same rebase. Also set `rebase-strategy: disabled` for GitHub Actions | Routine CI. The owner holds the PRs |
| 2 | OD-16: keeping container base images current | Dependabot version updates for the Docker images (already installed), then digest pinning. Leave Renovate inactive | Release engineering. No new external integration |
| 3a | Haptic confirmation | Option A: Android's touch feedback, no new permission | UX |
| 3b | The caller lists' "cleared" state | Option A: the existing headings, "No blocked numbers" and "No allowed numbers", without the body | UX copy |
| 3c | The screening-role screen after a decline | Option A: one past-tense block; the same two actions | UX copy and flow |
| 4 | OD-14 / F8: sign-out | Option A: sign out on this phone, from Settings. **It touches the Android session flow, so it is reviewed as high-risk before merge** | Authentication (client); high-risk by default |

---

## 1. OD-18: the held Dependabot PRs

**Source:** `ROADMAP-INVENTORY.md` (OD-18); `PRODUCTION-READINESS.md` F14. Each rebase by Dependabot re-runs full CI: on 2026-11-01, #184 and #185 alone queued about 186 `pr-go` jobs.

| PR | Change | Files | CI on its head | Breaking changes, from the release notes | Effect on this repository |
|---|---|---|---|---|---|
| #183 | `actions/upload-artifact` 4.6.2 → 7.0.2 | 5 workflows, including `release-android.yml` | 162 pass | v6: runs on Node 24 and needs runner ≥ 2.327.1. v7: the notes list no breaking item | None on GitHub-hosted runners. `release-android.yml` (manual) was **not exercised** by the PR's CI |
| #184 | `actions/setup-go` 5.6.0 → 7.0.0 | 6 workflows | 162 pass | v6: the default module cache is keyed on `go.mod`. v7: cache library updates | None. Every use sets `cache-dependency-path` explicitly |
| #185 | `actions/checkout` 4.4.0 → 7.0.1 | 18 workflows, including `release-android.yml` | 174 pass | v5: Node 24. v6: credentials persisted to a separate file. v7: `allow-unsafe-pr-checkout` (safer `pull_request_target` defaults) | None found. No workflow uses `pull_request_target`, and every checkout sets `persist-credentials: false`. `release-android.yml` was **not exercised** |
| #212 | `astral-sh/setup-uv` 5.4.2 → 10.3.3 | `pr-python.yml`, `python-deps-audit.yml` | 7 pass, including `pr-python` and the Python vulnerability audit | v6: venv activation becomes opt-in, and a default cache glob. v7: Node 24; `server-url` removed. v8: immutable releases; no minor tags; the custom manifest removed. v9: `prune-cache` defaults to false. v10: `enable-cache: auto` disables the cache on some events | None of the changed inputs is used. The repository sets `version: "0.5.14"`, `enable-cache: true` and `cache-dependency-glob` explicitly. v9 means **more GitHub Actions cache use** |

**Security advisories:** none. The GitHub Advisory Database lists none for any of the four actions (queried 2026-11-09).

**One gap in all four:** Dependabot rebased them before `evalstore-postgres.yml` was added (#273, 2026-11-09). That file still pins `checkout` v4.4.0 and `setup-go` v5.6.0. Merging as they are would leave the repository on mixed versions.

**Options:**

| Option | What | Consequence |
|---|---|---|
| **A (recommended)** | Rebase all four onto `main`. Dependabot does this with `@dependabot rebase`, and should cover the new workflow; otherwise, a follow-up commit. Merge each when its checks pass, **one at a time**. Then set `rebase-strategy: disabled` for `github-actions` in `.github/dependabot.yml` (F14) | Current, Node-24-ready actions everywhere. Each merge re-runs CI once. The first `release-android.yml` run after the merge is the only untested path, so watch it |
| B | Merge #183, #184 and #185; defer #212 | The same for three. #212 stays open and keeps being rebased, unless rebasing is disabled |
| C | Close all four and keep the current pins | No risk now. GitHub's move of runners from Node 20 to Node 24 will eventually force the update, under time pressure |

---

## 2. OD-16: keeping container base images current

**Source:**
- `PRODUCTION-READINESS.md` §3.8 and OD-16;
- `docker/go/Dockerfile` ("DIGEST PINNING IS NOT YET APPLIED");
- `renovate.json5`;
- `.github/dependabot.yml`.

**The facts that shape it:**
- **The shipped Go images are `FROM scratch`.** The `golang:1.26.10-alpine3.23` image is only the build stage. So **no base-image OS package ships** in the identity or edge-api image. What does ship is the Go toolchain's standard library, compiled into the binary. That follows `GO_VERSION` and is scanned by `govulncheck` on every Go PR, whatever the base image.
- **`docker/python/Dockerfile` (`python:3.12.8-slim-bookworm`) and `docker/Dockerfile.base` (`alpine:3.20`) do ship OS packages,** but **no CI job builds or tests them**. `container-images.yml` builds only `docker/go/Dockerfile`, for identity and edge-api.
- **Base images are pinned by tag, not digest.** On 2026-11-01 the registry's digest for `golang:1.26.10-alpine3.23` changed within a day: a tag is mutable.
- **Renovate is configured** (`pinDigests: true`, schedule, soak time, grouping) **but has never run.** It needs the Renovate GitHub App installed, which is an external integration. Its configuration covers **every** ecosystem (Go, Gradle, pip, npm, Actions, Docker), not only Docker.
- **Dependabot is already installed and active** (it opened #183–#185 and #212). Its `docker` entry (`/docker/go`, `/docker/python`) has `open-pull-requests-limit: 0`: security updates only, no version updates.

| | **Dependabot Docker version updates** | **Renovate** | **Manual** |
|---|---|---|---|
| New external integration | **None** (already installed) | **Yes**: the Renovate GitHub App (an external-integration approval) | None |
| Setup | Raise `open-pull-requests-limit` on the existing `docker` entry; weekly schedule; one-time digest pinning in the Dockerfiles | Install the app; restrict `renovate.json5` to Docker first (`enabledManagers`), or accept PRs across every ecosystem | None |
| Digest pinning | Dependabot updates `image:tag@sha256:…` references it finds. The first pin is a one-time change by us, resolved against the registry | `pinDigests` adds and maintains digests automatically | By hand: error-prone, and the reason the Dockerfile leaves digests out today |
| Update frequency | Weekly (configurable); security fixes as published | Configured: Monday before 06:00, 3-day soak; security at any time | Whenever someone remembers |
| Grouping and noise | Groups are supported. One Dockerfile directory produces few PRs | Strong grouping, a dependency dashboard, concurrency limits | — |
| Maintenance effort | Low: review one or two PRs a week | Low once tuned; initial tuning needed to avoid a PR flood | High, and it decays |
| Security coverage | GitHub's advisory database, integrated | The same advisories (`vulnerabilityAlerts`) | None automatic |
| Tests per update | `container-images.yml` builds and smoke-tests the Go images on `docker/go/Dockerfile` PRs. **The Python and base Dockerfiles have no CI build**, so their updates would be unverified | The same | The same |
| Rollback | Revert the PR. With digests, the earlier image is reproducible exactly | The same | The same, if digests were recorded |

**Recommendation: Dependabot Docker version updates, limited to `/docker/go`:**
1. Set `open-pull-requests-limit: 1` for `/docker/go`, weekly, on the existing entry. `/docker/python` stays security-only until it has a CI build.
2. Make a one-time change that pins `golang:1.26.10-alpine3.23` by digest, resolved from the registry and tested by `container-images.yml`. Update the Dockerfile's comment, which names Renovate.
3. Leave Renovate uninstalled. Correct the comments in `renovate.json5` and `dependabot.yml` that say Renovate owns routine updates.

**Consequences:**
- No new external account.
- The only shipped-image dependency, the builder, is kept current by a tested PR.
- The Python image stays unmaintained until it is built and tested in CI. That is a separate task once a Python service is deployed.
- **Go toolchain bumps** (`GO_VERSION`, and the `golang` tag with it) remain a deliberate change, as today.

---

## 3. Android UX choices

**Sources:**
- `docs/design/05-motion.md` §5.8;
- `docs/ux/android/05-screens-home-history-search.md` (A32);
- `docs/ux/android/08-user-flows.md` (F14);
- `docs/ux/android/06-screens-onboarding-auth-permissions.md` (A09b);
- `docs/ux/01-cross-surface-conventions.md` §1.2;
- `RELEASE-READINESS-ASSESSMENT.md` rows A09b and A32.

**Mockups below are text sketches.** No option is implemented, so there are no screenshots of the options.

### 3a. Haptic confirmation (`haptic.confirm`)

**Where the spec asks for it:**
- when the screening role comes back (F14: "granted → banner clears, haptic.confirm");
- when a number is added to a caller list (A32: "Add: row inserts, `haptic.confirm`").

**Today:** no haptics anywhere in the app.

**The spec conflicts with Android.**
- §5.8 maps `haptic.confirm` on **API 26–28** to `performHapticFeedback(CONFIRM)`. But `HapticFeedbackConstants.CONFIRM` and `REJECT` **exist only from API 30**.
- For API 30+ the spec uses `VibrationEffect` compositions, and for API 29 `EFFECT_CLICK`. Both go through `Vibrator`, which needs the **`VIBRATE` permission**.
- The app's permissions are pinned in `android/app/release-permissions.txt` and checked in CI, so adding one is a reviewed change, and it would appear on the Play listing.
- The project's Compose (BOM 2024.12.01, UI 1.7) has no confirm or reject haptic types (those arrived in Compose 1.8), so either option calls the platform directly.

| Option | API 30+ | API 26–29 | Permission | Honours the system "Touch feedback" setting | Notes |
|---|---|---|---|---|---|
| **A (recommended): touch feedback only** | `View.performHapticFeedback(CONFIRM)` | `performHapticFeedback(VIRTUAL_KEY)`, the spec's `haptic.click` level | **None** | **Yes, by the platform**, as §5.8 requires ("If haptic feedback is disabled, we emit nothing") | Smallest change; no data-safety or listing change. Older phones get a plain click rather than a distinct "confirm" |
| B: as the spec is written | `VibrationEffect` composition (click + tick) | API 29 `EFFECT_CLICK`; API 26–28 needs a substitute anyway | **`VIBRATE`** (install-time, no prompt; added to `release-permissions.txt`, shown on the listing) | **Not automatically.** The app must read and obey the setting itself; on API 33+ `VibrationAttributes.USAGE_TOUCH` helps | A richer feel on new phones; more code; a permission to justify |
| C: defer | — | — | — | — | Nothing ships. The spec rows stay "Not done" |

**Accessibility:**
- Haptics are supplementary. The banner change and the inserted row stay the visible and announced signals.
- With touch feedback off, nothing vibrates (§5.8).

**Whatever is chosen,** §5.8's API 26–28 cell needs correcting, because `CONFIRM` does not exist there. Under A, the 26–29 column becomes `VIRTUAL_KEY`.

### 3b. The caller lists' "cleared" state (A32)

**The spec:**
- A32 asks for a "Cleared tier if the user emptied it: one line, no action".
- §1.2's copy table gives "Blocklist emptied by user: **Blocklist is empty**".
- **The allowlist's line is not specified.**

**Today:** one empty state, the first-run one:

```
┌──────────────────────────────────────────┐
│ Blocked numbers                          │
│                                          │
│  No blocked numbers                      │  ← protection_list_empty_blocked_title
│  Numbers you block won't ring or be      │  ← …_empty_blocked_rings
│  screened.                               │
│  Blocking works on this phone, even      │  ← …_empty_blocked_offline
│  offline or if our service is down.      │
└──────────────────────────────────────────┘
```

**Consistency:** the app never says "blocklist" or "allowlist". It says "Blocked numbers" and "Allowed numbers" (the screen titles, "Move to allowed numbers", the messages). The spec's "Blocklist is empty" would be the only place the word appears.

| Option | Blocked numbers, cleared | Allowed numbers, cleared | New strings | Notes |
|---|---|---|---|---|
| **A (recommended)** | "No blocked numbers" (one line, no body) | "No allowed numbers" (one line, no body) | **None:** both headings exist and are already in the translation worksheets | Matches the screens' language. The cleared state is the first-run heading without its explanation, which someone who emptied the list has already seen |
| B: the spec's words | "Blocklist is empty" | "Allowlist is empty" (derived) | Two | Introduces "blocklist" and "allowlist"; regenerates the worksheets |
| C: no cleared tier | First-run text as today | First-run text as today | None | The spec row stays "Not done" |

**Sketch of A, after the person removes their last blocked number:**

```
┌──────────────────────────────────────────┐
│ Blocked numbers                          │
│                                          │
│  No blocked numbers                      │
│                                          │
└──────────────────────────────────────────┘
```

**What A or B needs:**
- **"Has this list ever had an entry?"** kept on the phone. That is one boolean per list, with no number in it. It is added to `ANDROID-DATA-INVENTORY.md`.
- **Undo still applies.** Removing the last number shows the 5-second undo first; the cleared state appears after it.

### 3c. The screening-role screen after a decline (A09b)

**The spec** (`06-screens…`, A09b edge cases):
- **previously denied:** the rationale shows once more, with the loss stated in the past tense ("You're currently missing…");
- **permanently denied:** the screen is **not** shown, and A74 handles it later in context.

**Today:**
- The screen appears after every successful sign-in while the role is not held (`AegisNavGraph`, `PHONE_VERIFICATION` → `SCREENING_ROLE`). That is when a previous decline can be met again: a new sign-in on the same phone.
- It always shows the same four blocks. Its "If you say no" block reads: "Every unknown call gets forwarded, which may cost you carrier charges and means people you know get screened too."
- The app records no decline. `ScreeningRole.status` knows only HELD, NOT_HELD and UNAVAILABLE.

**An honesty point the owner should know:** call forwarding is **not active**; it is release milestone M3. "Every unknown call gets forwarded" describes the designed product, not what happens today. A past-tense version must not say forwarding is happening.

| Option | The fourth block after a decline | Actions | Recovery |
|---|---|---|---|
| **A (recommended)** | Label: **"What you're missing"**. Text: **"You're currently missing on-device blocking: numbers you've blocked can still ring, and calls aren't checked on this phone."** | Unchanged: "Continue" (the system's role request) and "Not now" | If the system then refuses without asking (below), the person can still turn blocking on from Protection ("Turn on blocking"), the existing A74 path |
| B | The same screen as the first time, unchanged | Unchanged | As A |
| C | Skip the screen after any earlier decline | — | Only Protection's notice. Fewer prompts, but the spec's "shows once more" is not met |

**Sketch of A:**

```
┌──────────────────────────────────────────┐
│ Screen calls on your phone               │
│                                          │
│ What Android will ask                    │
│ Android will ask to make this app your   │
│ caller ID & spam app.                    │
│ What it enables                          │
│ We can block numbers you've blocked, …   │
│ What we'll never do                      │
│ We can't hear your calls. …              │
│ What you're missing                      │  ← replaces "If you say no"
│ You're currently missing on-device       │
│ blocking: numbers you've blocked can     │
│ still ring, and calls aren't checked on  │
│ this phone.                              │
│                                          │
│ [ Continue ]              [ Not now ]    │
└──────────────────────────────────────────┘
```

**What A needs:**
- A local "declined once" flag, set when the role request returns without the role. It is one boolean, added to the data inventory.
- `ScreeningRoleCopyTest` extended to the new block.

**"Permanently denied" cannot be read from Android.** There is no API that says a role request will be refused without a dialog. On some versions Android stops showing the dialog after repeated refusals; *unverified on this project's devices*. The practical rule is: show A once after a decline, and never again. Protection's notice remains the way back.

---

## 4. OD-14 / F8: sign-out

**Source:**
- `PRODUCTION-READINESS.md` F8;
- `ANDROID-DATA-INVENTORY.md` D4;
- `IdentityAuthenticator.signOut()`;
- identity's routes;
- `ROADMAP-INVENTORY.md` C10 and O17.

**Current behaviour:**
- **The app has no sign-out action.** `IdentityAuthenticator.signOut()` exists and is unit-tested (`signOutAndRedaction`), but nothing calls it.
- **What `signOut()` does:**
  - clears the encrypted session on the phone (the session id, the access token and the refresh token);
  - sets the state to "signed out on this device";
  - does **not** destroy the Keystore device key; only a server-confirmed account deletion does.
- **Identity has no sign-out endpoint.** The routes are OTP request and validate, refresh, account deletion, and web deletion.
- **So a sign-out today would end nothing on the server:**
  - the session and its refresh-token hash stay until they expire;
  - an access token already issued stays valid for up to its 15-minute lifetime (`accesstoken.Lifetime`).
- **Today's exits are:**
  - account deletion, which ends everything;
  - a session ending on the server, which shows the A73 signed-out notice.

**The residual risk of a phone-only sign-out is low:**
- **Refresh needs two things:** the refresh token, which sign-out deletes from the phone, **and** a proof signed by the device key, which never leaves the Keystore.
- **A copied refresh token alone cannot refresh.**
- **What remains:** an access token copied before sign-out works until it expires (≤ 15 min). That is the same whether or not sign-out exists. Revoking access tokens is C10, which needs O17, a high-risk ADR-0010 amendment.

| Option | What | Session invalidation | Local data | Review |
|---|---|---|---|---|
| **A (recommended)** | A "Sign out" action in Settings, with a confirmation; it calls the existing `signOut()` and returns to Welcome | **On this phone only:** the session is deleted here. The server session expires on its own, and cannot be refreshed without the device key | Keep the phone's own data (blocked and allowed numbers, language, screening role): they belong to the phone, not the account, and on-device blocking keeps working. The confirmation says so | **High-risk by default** (Android session and authentication flow): the independent review (OD-12) before merge |
| B | A, plus an identity endpoint that revokes the session (`POST /v1/session/revoke`, proof-bound) | The server session ends at once; access tokens still last ≤ 15 min unless C10/O17 is also done | As A | High-risk: a new authentication endpoint, the API contract, OD-12 |
| C | No sign-out (as today) | — | — | None. F8 stays open. Account deletion remains the only way to leave |

**Local-data questions inside A (the owner's):**
1. Keep or clear the blocked and allowed numbers on sign-out. *Recommended: keep;* clearing them would silently turn blocking off.
2. Keep or discard the device key. *Recommended: keep.* Discarding it makes the next sign-in enrol a new key, which identity records as a re-enrolment with another key, an audit signal meant for real device changes.

**Tests that A needs:**
- A Robolectric test: Settings → Sign out → confirm → Welcome; the session store is empty; Back does not return to a signed-in screen.
- The lists are unchanged after sign-out.
- **A device test on the emulator:** sign out, kill the app, start it cold; it starts signed out.
- The existing unit test `signOutAndRedaction` stays.
- No server test is needed for A.

---

## 5. Decisions recorded

### OD-16: base-image maintenance (owner, 2026-11-10): approved as recommended

- **The Go builder image** (`docker/go/Dockerfile`):
  - Dependabot version updates for `/docker/go`;
  - the existing daily schedule;
  - one PR at a time;
  - patch releases only. A new Go minor or major stays a deliberate change, made together with `GO_VERSION`.
- **Pinned by digest:** `golang:1.26.10-alpine3.23@sha256:78e7f1e419545bc4b8b5ee8a40a99ddb5df1216d38681dbab2eeb1fdb2427f43`.
  - It is the multi-architecture index, read on 2026-11-10 from the registry and from the Docker Hub API, which agreed.
  - Its amd64 and arm64 images are Go 1.26.10 on Alpine 3.23.8.
- **The Python image:** security updates only.
- **Renovate:** not installed.
- **F14** (Dependabot's rebase strategy) is a separate decision and is unchanged.
- Recorded with its implementation; see `DEPENDENCY_AUDIT.md` §13.

## After the owner decides

Each decision is recorded in this packet's §5 (to be added) with its date. Only then is the chosen option implemented, one PR per decision. Each:
- runs its tests and every required check;
- is merged only when green. Option 4A or 4B is merged only after the independent review.
