# ADR-0024: One Kubernetes manifest tree, `infra/k8s`

- **Status:** Proposed. **NOT accepted.** This ADR records a recommendation for the owner's decision. Until it is accepted, nothing below is in force.
- **Date:** 2026-10-20
- **Deciders:** the repository owner (architecture authority; see "Approval required")
- **Consulted:** _none yet_
- **Informed:** SRE, Security (`/infra/` paths in `.github/CODEOWNERS`)

**Numbering:** 0018–0023 are already used by unpublished Phase 16 ADRs that are not on `main`, so this proposal takes the next number after them. The owner may renumber it when accepting.

---

## Context

The repository names two different directories as the home of its Kubernetes manifests, and neither naming has been decided.

**Tracked on `main` (`e0b0c2e`):**

| Where | What it says |
|---|---|
| `infra/k8s/base/{configmap,deployment,namespace}.yaml` | **The only tracked Kubernetes manifests.** `deployment.yaml` is a shared template (`aegis-service-template`). |
| `docker/compose/docker-compose.yml:7-8` | "Production runs on Kubernetes from infra/kubernetes." |
| `docs/telephony/P1-41_INFRA_AUTH_READINESS_AUDIT.md:18,68` | names `infra/kubernetes/overlays/staging/` as the place for the staging manifests (A5) |
| `docs/phase15/READINESS_AUDIT.md`, P1-40…P1-50 | refer to `infra/k8s/base/...` |
| `Taskfile.yml` | has Terraform and Helm targets, and no Kubernetes-manifest target |
| ADR-0008 | selects EKS on Graviton; it names no manifest directory, no Kustomize or Helm, and no GitOps tool |

Nothing on `main` validates manifests: there is no `pr-infra` workflow.

**In the owner's working tree, untracked (not on `main`, so not authoritative):**
- **`infra/README.md`:** "`infra/k8s` is the authoritative Kubernetes tree" and "There is no second tree". It warns that if `infra/kubernetes` (or `infra/helm`, …) acquires files, "it becomes a second apparently authoritative source of deployment truth", and that "a change of authority belongs in an ADR".
- **`infra/k8s/base/services/*.yaml`:** 16 per-service Deployments and PDBs, generated from one template.
- **`.github/workflows/pr-infra.yml`:** kubeconform over `infra/`, and an image-digest check over `infra/k8s/base/services`.
- **`infra/k8s/check-image-references.sh`.**
- **The modified `Taskfile.yml`:** `infra:k8s:validate`.
- **The design records P1-37 and P1-39:** "Staging manifests go in **`infra/kubernetes/overlays/staging/`**" and "`infra/k8s/base/services/` is left untouched".
- **Empty directories:** `infra/kubernetes/overlays/{dev,staging,prod-in,prod-global}`. Git does not track empty directories.

So the tracked manifests, and all of the owner's manifest tooling, live in `infra/k8s`, while one tracked comment, one tracked audit and the staging design records point at `infra/kubernetes`. The first staging manifest (A5) cannot be written until one tree is chosen: writing it under `infra/kubernetes` would create exactly the second source of truth that `infra/README.md` warns against.

## Decision Drivers

1. **One source of deployment truth.** A reviewer, CI and an operator must agree on which file is applied.
2. **Agreement with what is tracked and with the work in progress.** Moving files costs review effort and risks conflicts with uncommitted work.
3. **Staging isolation (P1-37 C2, P1-39 O-1).** Staging manifests must be staging-only, and creating them must not change the production base.
4. **Enforceability.** The choice must be checkable in CI, not just written down.
5. **Fit with Kustomize's base-and-overlays layout,** which the staging design assumes.

## Considered Options

1. **`infra/k8s` is the only tree; environment overlays live at `infra/k8s/overlays/<env>/`.**
2. **`infra/kubernetes` is the only tree;** the tracked `infra/k8s` and the owner's untracked `infra/k8s` work are moved into it.
3. **Both trees:** base in `infra/k8s`, overlays in `infra/kubernetes/overlays/` (the literal reading of P1-37/P1-39 today).

## Decision Outcome (proposed)

**Recommended: Option 1.** The decision is the owner's; this records a recommendation only.

**Why, in terms of the drivers:**
1. **One tree:** Option 1 is the only option that leaves exactly one manifest directory.
2. **Agreement with what exists:** every tracked manifest, and every piece of the owner's in-progress manifest tooling, is already in `infra/k8s`. Option 1 moves nothing, and needs two tracked text corrections (below). Option 2 would move every tracked and untracked file and rewrite its tooling and documents.
3. **Staging isolation is unchanged:** `infra/k8s/overlays/staging/` is just as staging-only as `infra/kubernetes/overlays/staging/`. The P1-37/P1-39 constraints carry over unchanged: base left untouched, a self-contained A/B `Recreate` overlay, and `deployment.yaml` never restored. Only the path changes.
4. **Enforceable:** with one root, "no manifest outside `infra/k8s`" is a one-line CI check.
5. **Kustomize fit:** `infra/k8s/{base,overlays/<env>}` is the conventional layout, and an overlay can reference `../../base` when one is meant to.

**What acceptance would change (each is a separate, reviewable change made after acceptance, not before):**
- **Amend the P1-37/P1-39 staging path** to `infra/k8s/overlays/staging/`. The originals are untracked owner documents, so the amendment is the owner's to record, and the originals are preserved.
- **Correct `docker/compose/docker-compose.yml:8`** to name `infra/k8s`.
- **Note in `docs/telephony/P1-41…:68`, or its successor,** that the staging path is `infra/k8s/overlays/staging/`. Accepted records are not rewritten; a successor or erratum is added.

**Enforcement (proposed; to land together with the first tracked validation workflow):**
- **No manifests outside `infra/k8s`:** CI fails if any YAML containing `apiVersion:` and `kind:` is tracked outside `infra/k8s/`, ignoring `.github/` and `docker/compose/`, or if anything is tracked under `infra/kubernetes/`.
- **No stale path references:** CI fails on any tracked reference to `infra/kubernetes` other than this ADR and its successors.
- **Schema validation:** kubeconform runs over `infra/k8s/`. CRD kinds (`ExternalSecret`, `Kustomization`) are resolved from pinned schemas, or explicitly allow-listed. That policy is a separate decision, because `-ignore-missing-schemas` would also hide typos.
- **Image references:** the image-digest check covers `infra/k8s/overlays/**` as well as `infra/k8s/base/services/`.

### Consequences

**Positive**
- One directory to review, validate and apply. A5 can name a path.
- No tracked or in-progress file moves.

**Negative**
- The staging path in P1-37/P1-39 (and P1-41:68) must be amended, so the decision record differs from the design record until then.
- The empty local `infra/kubernetes/` directories should be removed by the owner. They are untracked, so this is local housekeeping only.

**Neutral**
- Nothing about staging's content, the base manifest (F-7), or any approval (A5–A9, INFRA-AUTH, SEP, O-4, D10) changes.

### Confidence

**Medium-high.** The facts are verified on `main`. The owner's untracked work strongly suggests `infra/k8s` was already the intended tree, but that work is not authoritative until committed.

### Revisit Trigger

A decision to adopt Helm, or a GitOps tool that prescribes its own repository layout, would trigger a revisit; so would a requirement to keep production and staging manifests in separate repositories.

## Options in Detail

### Option 1: `infra/k8s` only, overlays under `infra/k8s/overlays/<env>/`

- **Good:** matches every tracked manifest and all in-progress tooling. No moves. One root to enforce.
- **Bad:** contradicts the literal path in P1-37/P1-39 and P1-41, which need amending; contradicts the docker-compose comment, which needs correcting.

### Option 2: `infra/kubernetes` only

- **Good:** matches the docker-compose comment and the literal P1-37/P1-39/P1-41 path.
- **Bad:** moves the tracked `infra/k8s/base`, and forces a move of 16 untracked owner manifests, the README, the validation workflow, the image checker and the Taskfile target. It also leaves `infra/k8s/...` references in eight tracked documents, which can only be annotated, not edited. The highest churn and the highest conflict risk with uncommitted work.

### Option 3: both trees

- **Good:** no edits to any record.
- **Bad:** two apparent sources of truth, which is the outcome `infra/README.md` exists to prevent. Every check and every reviewer must know which tree holds what. Rejected.

## Approval required

| Decision | Authority | Status |
|---|---|---|
| Accept, amend or reject this ADR (and its number) | repository owner (architecture) | **pending** |
| Amend the P1-37/P1-39 staging path | repository owner (author of those records) | pending; depends on this ADR |
| kubeconform CRD-schema policy | owner / SRE | pending |
| A5 (staging manifests) and every later gate | owner, per P1-38 §2 | **unapproved**; this ADR does not approve any of them |

## References

- ADR-0008 (cloud infrastructure)
- `docker/compose/docker-compose.yml`
- `docs/telephony/P1-41_INFRA_AUTH_READINESS_AUDIT.md`
- the owner's untracked `infra/README.md`, P1-37, P1-38 and P1-39 (not on `main`)
- supersedes: none
