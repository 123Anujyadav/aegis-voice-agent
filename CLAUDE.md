# CLAUDE.md: working instructions for Claude Code in this repository

These are working instructions for an AI coding agent. They do not grant any
GitHub permission. They do not override branch protection, rulesets, required
checks, review requirements, tool-permission prompts or any other safety
control. Where they conflict with an explicit instruction from the repository
owner, the owner's instruction wins. For project conventions, follow
`CONTRIBUTING.md` and `SECURITY.md`; the "Known gaps" section below lists where
they and the repository's live state differ.

## 1. Workflow for routine, in-scope work

1. **Inspect before editing:**
   - `git status`, the current branch and HEAD;
   - `git fetch` and the `origin/main` SHA;
   - the worktree list;
   - open PRs that touch the same files.

   Treat every uncommitted or untracked file you did not create as the owner's work.
2. **Plan:** state the change, the files it touches, and how it will be verified.
3. **Isolate:** work in a new branch and a separate `git worktree` created from `origin/main`. Never edit a worktree that holds someone else's uncommitted changes. One focused change per branch and PR.
4. **Implement** only what is in scope. Before editing a file, check that no uncommitted change, open PR or preservation branch modifies it.
5. **Verify:**
   - Go: `gofmt -l`, `gofumpt -l`, `go vet`, `golangci-lint run` in each touched module;
   - the module's tests, with `-race`, and `-shuffle=on` where it helps;
   - Python: the relevant `pytest`, `ruff` and `mypy`;
   - database suites against a throwaway container, never a shared database;
   - for lint work, confirm that the findings removed are exactly the intended ones and that none were added.
6. **Review your own diff** before committing: no unrelated files, no weakened assertions, no new suppressions, no secrets.
7. **Commit** with Conventional Commits (see "Known gaps" for scopes), then push the branch normally and open or update a focused PR.
8. **Keep the PR description current:** what changed, the exact test and lint evidence, the risks, and what remains.
9. **Report** only what you verified, with the evidence (commands, run IDs, SHAs, counts). Say plainly when something was not run, could not be verified, or failed.
10. **Continue** with the next independent, safe task without asking after every routine step. Stop only at an approval boundary (§4).

## 2. PRs and CI

- **Before acting on a PR,** verify:
  - its base branch and exact head SHA;
  - its mergeability and its checks.

  Re-verify the head immediately before any merge someone has authorized.
- **Wait** for every relevant check to reach a terminal state. Check runs paginate; read every page.
- **On a failure:**
  - read the job log;
  - compare it with the same job on `main`, where many lint jobs fail as a known baseline;
  - fix the failure if it is in scope.

  Re-run only for a demonstrated infrastructure or flake cause, and record it as a flake. Never re-run until green to hide a real failure.
- **What passing checks mean:** that the configured checks passed, nothing more. It does not mean every test ran (some suites skip without a database), that lint is clean, or that any readiness or sign-off criterion is met.
- **Merging:** never merge unless the owner has explicitly authorized that specific PR, or a clearly defined class of PRs under stated conditions, and every condition is re-verified at merge time.
  - Use squash merges with the head SHA pinned (`gh pr merge --squash --match-head-commit <sha>`).
  - Then check CI on `main`.
- **Never bypass** branch protection, rulesets, required checks, reviews or repository permissions. Do not use admin merge.

## 3. Preservation and safety

- **Never run these without explicit authorization for that action:**
  - `git reset`, `git clean`, `git stash`, `git checkout -- <path>` or `git restore` over someone else's changes;
  - amend, rebase, `push --force`, or any other history rewrite;
  - deleting branches or worktrees you did not create.
- **Do not restore deleted files you did not delete.** An unexplained deletion in the owner's worktree may be intentional.
- **Preserve:**
  - preservation branches (`preserve/*`);
  - existing worktrees;
  - untracked documents;
  - conflicting file versions. Do not resolve a conflict by picking one side.
- **Never print, log or commit tokens or credentials,** and never ask anyone to paste a secret into chat. Do not disable TLS verification.
- **Without explicit authorization, do not:**
  - deploy;
  - place live carrier or telephony calls;
  - change production or staging infrastructure;
  - incur cloud spending;
  - provision providers, models or credentials;
  - download models;
  - expose a service publicly.
- **Do not change** repository settings, branch protection, rulesets, CODEOWNERS ownership or CI toolchain policy unless that specific change has been authorized.
- **Do not weaken tests.** Do not add `//nolint`, `# noqa` or `@Suppress` without an inline reason (`CONTRIBUTING.md`) and a justification in the PR. Do not change lint policy in order to get CI green.

## 4. Approval boundaries

**May proceed without asking, when the work does not overlap anyone else's
work:**
- reading the repository, CI logs and the GitHub API;
- local builds, tests and lint;
- throwaway local test containers;
- safe lint fixes and test-only or doc-only fixes within an agreed scope;
- new branches and worktrees;
- normal (non-force) pushes of your own branches;
- opening or updating PRs and their descriptions;
- collecting and recording evidence.

**Ask first:**
- an architecture or design decision, an ADR-level change, or a contract change;
- an ambiguous requirement;
- anything destructive or hard to reverse;
- production or staging actions;
- spending, credentials or external accounts;
- any merge;
- any owner sign-off or risk acceptance.

**Never infer** merge permission, architecture approval, risk acceptance or a
sign-off from "continue", "proceed" or similar. Record unresolved blockers
honestly. Do not mark them resolved without evidence and the owner's decision.

## 5. Project constraints

- **The `speech` package (`packages/go/speech`) is frozen**
  (`docs/audio-intelligence/AUDIO_INTELLIGENCE_ARCHITECTURE.md`). Change its code or tests only with explicit authorization for that change.
- **Staging and Exotel AgentStream production media are NO-GO** until their documented blockers are independently resolved and approved (`docs/phase15/READINESS_AUDIT.md` §3.12–§3.13).
- **D8** (gateway CI and `-race` evidence) needs the owner's explicit sign-off. Never record it on the owner's behalf. Do not change the Go `go` directives, the CI Go version or the D8 criterion to satisfy it.
- **Do not decide** D9, D10, D11, `prod-global`, F-3, F-4, F-7, INFRA-AUTH, provider or model choices, or Exotel client-certificate mTLS.
- **The outbox retry and dead-letter policy** (ADR-0009 follow-up) is an owner decision. Do not implement retry or DLQ behaviour without one.
- **Do not edit** `docs/phase14/FINAL_REPORT.md`.

## 6. Environment notes

- **Windows host:**
  - Bash is Git Bash, so paths need `cygpath` and `MSYS_NO_PATHCONV=1` for Docker and `git show <rev>:<path>`;
  - Windows Python cannot see Git Bash's `/tmp`;
  - PowerShell `Set-Content -Encoding utf8` writes a BOM.
- **There is no native C toolchain,** so run `-race` in Docker with `golang:1.26.9-bookworm`, mounting the source read-only and copying it inside. Keep container concurrency low.
- **Do not run** repository-wide `task format`, `task go:tidy` or `task clean`. They touch files outside your change.

## 7. Known gaps between the guidance and live state (verify; do not assume)

- **Merge queue and CODEOWNERS:** `CONTRIBUTING.md` describes a merge queue and required CODEOWNERS approval. Check the live branch protection, rulesets and `GET /repos/{owner}/{repo}/codeowners/errors` instead of assuming either.
- **Commit scopes:** `commitlint.config.mjs` defines a closed scope list, but no CI workflow runs commitlint, and `main`'s history uses scopes outside it (for example `lint`, `phase15`, `telephony`). Prefer a listed scope when one fits. Do not edit the scope list without the owner.
- **Flaky tests:** `CONTRIBUTING.md` asks for them to be quarantined within 24 hours. Do not skip, quarantine or delete a test, frozen or not, without explicit authorization. Report the flake with its evidence instead.
