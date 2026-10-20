# ADR-0024: One Kubernetes manifest tree, `infra/k8s`

- **Status:** Proposed. **NOT accepted.** This ADR records a recommendation for
  the owner's decision. Until it is accepted, nothing below is in force.
- **Date:** 2026-10-20
- **Deciders:** the repository owner (architecture authority; see "Approval
  required")
- **Consulted:** _none yet_
- **Informed:** SRE (owns `/infra/` in `.github/CODEOWNERS:79`); Security
  (owns `/infra/policy/` and the production Terraform environments,
  `.github/CODEOWNERS:80-82`)

**Numbering:** 0018–0023 are already used by unpublished Phase 16 ADRs that are
not on `main`, so this proposal takes the next number after them. The owner may
renumber it when accepting.

---

## Context

The repository names two different directories as the home of its Kubernetes
manifests. No tracked record decides between them. The owner did approve the
staging path `infra/kubernetes/overlays/staging/`, at design level only, in the
untracked P1-37 (2026-10-15). P1-38:18 lists it as "Already approved (design
only, P1-37)". No rationale or comparison of paths is recorded in P1-33…P1-39.
**This ADR therefore proposes amending an approved design decision,** not
filling a gap.

**Tracked on `main` (`e0b0c2e`):**

| Where | What it says |
|---|---|
| `infra/k8s/base/{configmap,deployment,namespace}.yaml` | **The only tracked Kubernetes manifests.** `deployment.yaml` is a shared template (`aegis-service-template`). |
| `docker/compose/docker-compose.yml:7-8` | "Production runs on Kubernetes from infra/kubernetes." |
| `docs/telephony/P1-41_INFRA_AUTH_READINESS_AUDIT.md:68` | names `infra/kubernetes/overlays/staging/` as the place for the staging manifests (A5); line 18 records the `infra/kubernetes/overlays/*` directories as empty and untracked |
| `docs/phase15/READINESS_AUDIT.md`, P1-40…P1-50 (eight documents) | refer to `infra/k8s/base/...` |
| `Taskfile.yml:432-435` | `infra:helm:lint` runs `helm lint infra/helm/callscreen-service`, a **third** candidate home, for a Helm chart that does not exist |
| `.gitignore:115-117`, `.pre-commit-config.yaml:34-37` | ignore rendered charts under `infra/helm/`, and exclude `infra/helm/*/templates/` from YAML checks |
| `Taskfile.yml` `infra:lint` | described as "Validate Terraform formatting and Kubernetes policy", but runs only the Terraform tasks; there is no Kubernetes-manifest target |
| ADR-0008 | selects EKS on Graviton; it names no manifest directory, no Kustomize or Helm, and no GitOps tool |

Nothing on `main` validates manifests: there is no `pr-infra` workflow. The only
tracked YAML files declaring both `apiVersion:` and `kind:` are the three under
`infra/k8s/base/`.

**In the owner's working tree, untracked (not on `main`, so not
authoritative):**
- **`infra/README.md`:** "`infra/k8s` is the authoritative Kubernetes tree" and
  "There is no second tree". It warns that if `infra/kubernetes` (or
  `infra/helm`, …) acquires files, "it becomes a second apparently authoritative
  source of deployment truth", and that "a change of authority belongs in an
  ADR".
- **`infra/k8s/base/services/*.yaml`:** 16 per-service Deployments and PDBs,
  generated from one template.
- **`.github/workflows/pr-infra.yml`:** kubeconform over `infra/`, and an
  image-digest check over `infra/k8s/base/services`.
- **`infra/k8s/check-image-references.sh`.**
- **The modified `Taskfile.yml`:** `infra:k8s:validate`.
- **The design record P1-37** (lines 35 and 59-61): staging uses "a **new
  staging-only manifest path**"; "Staging manifests go in
  **`infra/kubernetes/overlays/staging/`**"; "**`infra/k8s/base/services/` is
  left untouched**"; and "`infra/k8s/base/deployment.yaml` is never restored".
- **The decisions record P1-39:**
  - **O-1** (line 24): gateways A and B are two separately named single-replica
    workloads with `strategy: Recreate`, on separate Graviton nodes, from one
    recorded image digest, against the direct PostgreSQL writer endpoint, with
    synthetic data only; staging manifests live only under
    `infra/kubernetes/overlays/staging/`.
  - **O-4** (line 31, conditional): the lock key's single shared source is one
    ConfigMap in that staging overlay.
- **Empty directories:**
  `infra/kubernetes/overlays/{dev,staging,prod-in,prod-global}`. Git does not
  track empty directories.

So the tracked manifests, and all of the owner's manifest tooling, live in
`infra/k8s`. One tracked comment, one tracked audit and the staging design
records point at `infra/kubernetes`, and tracked tooling points at a Helm chart
under `infra/helm`. The first staging manifest (A5) cannot be written until one
tree is chosen: writing it under `infra/kubernetes` would create exactly the
second source of truth that `infra/README.md` warns against.

### What is established, and what is not

| Kind | Content |
|---|---|
| **Verified on `main`** | every row of the tracked table above |
| **Owner documents, not on `main`** | `infra/README.md`, the 16 service manifests, `pr-infra.yml`, the image checker, the Taskfile change, P1-37, P1-38, P1-39. Their content is quoted here, but they carry no authority until committed or confirmed by the owner. |
| **Assumption A1** | P1-37's requirement of "a new staging-only manifest path" is about the staging manifests being separate from the production base, not about their living outside `infra/k8s`. `infra/k8s/overlays/staging/` satisfies the first reading; only the owner can confirm it was the intended one. |
| **Assumption A2** | Helm is not intended now. **Evidence:** no Helm chart exists on any branch, and no ADR or decision record selects Helm (ADR-0001 and ADR-0008 name none). The owner's local Taskfile change makes `infra:helm:lint` refuse to run because "No Helm chart is present". **Counter-evidence:** Helm is named as planned structure in `ARCHITECTURE_FREEZE.md:215` (§9, item 9, owner SRE: "Terraform, Kubernetes and Helm are structural only — no real infrastructure yet") and `README.md:55,148`. Whether that intent still stands is the owner's and SRE's call. |
| **Unresolved conflict U1** | the literal staging path in P1-37/P1-39/P1-41 vs the tracked `infra/k8s` tree |
| **Unresolved conflict U2** | the Helm references (Taskfile, `.gitignore`, pre-commit) vs a tree with no chart |

## Decision Drivers

1. **One source of deployment truth.** A reviewer, CI and an operator must agree
   on which file is applied.
2. **Agreement with what is tracked and with the work in progress.** Moving
   files costs review effort and risks conflicts with uncommitted work.
3. **Staging isolation (P1-37 C2, P1-39 O-1).** Staging manifests must be
   staging-only, and creating them must not change the production base.
4. **Enforceability.** The choice must be checkable in CI, not just written
   down.
5. **Fit with Kustomize's base-and-overlays layout,** which the staging design
   assumes.

## Considered Options

1. **`infra/k8s` is the only tree; environment overlays live at
   `infra/k8s/overlays/<env>/`.**
2. **`infra/kubernetes` is the only tree;** the tracked `infra/k8s` and the
   owner's untracked `infra/k8s` work are moved into it.
3. **Both trees:** base in `infra/k8s`, overlays in `infra/kubernetes/overlays/`
   (the literal reading of P1-37/P1-39 today).

## Decision Outcome (proposed)

**Recommended: Option 1.** The decision is the owner's; this records a
recommendation only.

**Why, in terms of the drivers:**
1. **One tree:** Option 1 is the only option that leaves exactly one manifest
   directory.
2. **Agreement with what exists:** every tracked manifest, and every piece of
   the owner's in-progress manifest tooling, is already in `infra/k8s`. Option 1
   moves nothing; it needs only the text and tooling corrections listed below.
   Option 2 would move every tracked and untracked file and rewrite its tooling
   and documents.
3. **Staging isolation is unchanged:** `infra/k8s/overlays/staging/` is just as
   staging-only as `infra/kubernetes/overlays/staging/` (subject to assumption
   A1). Every existing constraint carries over unchanged, and none is added:
   - **P1-37:** a staging-only path; `infra/k8s/base/services/` left untouched;
     `infra/k8s/base/deployment.yaml` never restored.
   - **P1-39 O-1:** A and B as separately named single-replica `Recreate`
     workloads on separate Graviton nodes, from one image digest, against the
     direct writer endpoint, with synthetic data only.
   - **P1-39 O-4 (still conditional):** one shared lock-key ConfigMap in the
     staging overlay.

   **Only the path changes.** How the overlay is built (whether it references
   the base, its resources and its choices) remains A5's to approve.
4. **Enforceable:** with one root, "no manifest outside `infra/k8s`" is a
   one-line CI check.
5. **Kustomize fit:** `infra/k8s/{base,overlays/<env>}` is the conventional
   layout, and an overlay can reference `../../base` when one is meant to.

**What acceptance would change (each is a separate, reviewable change made after
acceptance, not before):**
- **Amend the P1-37/P1-39 staging path** to `infra/k8s/overlays/staging/`. The
  originals are untracked owner documents, so the amendment is the owner's to
  record, and the originals are preserved.
- **Correct `docker/compose/docker-compose.yml:8`** to name `infra/k8s`.
- **Note in `docs/telephony/P1-41…:68`, or its successor,** that the staging
  path is `infra/k8s/overlays/staging/`. Accepted records are not rewritten; a
  successor or erratum is added.
- **Resolve the Helm references (U2).** Under assumption A2, remove the
  `infra:helm:lint` target and the `infra/helm` ignore and pre-commit entries.
  If Helm is wanted, that is its own ADR, and the chart lives under `infra/k8s`
  too.
- **Make `infra:lint` validate the Kubernetes manifests it claims to,** through
  the owner's `infra:k8s:validate` once it is committed.

**Enforcement (proposed; to land together with the first tracked validation
workflow):**
- **No manifests outside `infra/k8s`:** CI fails if any YAML declaring both
  `apiVersion:` and `kind:` is tracked outside `infra/k8s/` (today, none is), or
  if anything is tracked under `infra/kubernetes/` or `infra/helm/`.
- **No stale path references:** CI fails on any tracked reference to
  `infra/kubernetes` or `infra/helm` other than this ADR and its successors.
- **Schema validation:** kubeconform runs over `infra/k8s/`. Two cases are
  distinct, and the policy for them is a separate decision:
  - **`kustomization.yaml`** is build configuration, not a cluster object. It is
    skipped by kind, or the overlays are validated as rendered
    (`kustomize build`) output.
  - **CRD kinds such as `ExternalSecret`** are validated against pinned schemas,
    or explicitly allow-listed.
  - A blanket `-ignore-missing-schemas` is not proposed: it would also pass a
    misspelt `kind`.
- **Image references:** the image-digest check covers `infra/k8s/overlays/**` as
  well as `infra/k8s/base/services/`.

Every enforcement item lands only with, or after, the first tracked
infra-validation workflow. None is part of accepting this ADR.

### Consequences

**Positive**
- One directory to review, validate and apply. A5 can name a path.
- No tracked or in-progress file moves.

**Negative**
- The staging path in P1-37/P1-39 (and P1-41:68) must be amended, so the
  decision record differs from the design record until then.
- The Helm references (Taskfile, `.gitignore`, pre-commit) must be removed or
  justified by a Helm ADR.
- The empty local `infra/kubernetes/` directories should be removed by the
  owner. They are untracked, so this is local housekeeping only.

**Neutral**
- Nothing about staging's content, the base manifest (F-7), or any approval
  (A5–A9, INFRA-AUTH, SEP, O-4, D10) changes.

### Confidence

**Medium-high.** The tracked facts are verified on `main` (`e0b0c2e`). The
owner's untracked work strongly suggests `infra/k8s` was already the intended
tree, but it carries no authority until committed. The recommendation also
rests on assumptions A1 and A2, which only the owner can confirm.

### Revisit Trigger

A decision to adopt Helm, or a GitOps tool that prescribes its own repository
layout, would trigger a revisit; so would a requirement to keep production and
staging manifests in separate repositories.

## Options in Detail

### Option 1: `infra/k8s` only, overlays under `infra/k8s/overlays/<env>/`

- **Good:** matches every tracked manifest and all in-progress tooling. No
  moves. One root to enforce.
- **Bad:** contradicts the literal path in P1-37/P1-39 and P1-41, which need
  amending; contradicts the docker-compose comment, which needs correcting;
  leaves the Helm references to resolve (U2); rests on assumption A1.

### Option 2: `infra/kubernetes` only

- **Good:** matches the docker-compose comment and the literal P1-37/P1-39/P1-41
  path.
- **Bad:** moves the tracked `infra/k8s/base`, and forces a move of 16 untracked
  owner manifests, the README, the validation workflow, the image checker and
  the Taskfile target. It also leaves `infra/k8s/...` references in eight
  tracked documents, which can only be annotated, not edited. The highest churn
  and the highest conflict risk with uncommitted work.

### Option 3: both trees

- **Good:** no edits to any record.
- **Bad:** two apparent sources of truth, which is the outcome `infra/README.md`
  exists to prevent. Every check and every reviewer must know which tree holds
  what. Rejected.

## Approval required

| Decision | Authority | Status |
|---|---|---|
| Accept, amend or reject this ADR (and its number) | repository owner (architecture) | **pending** |
| Confirm assumption A1 (what P1-37's "staging-only path" requires) | repository owner (author of P1-37) | **pending** |
| Confirm assumption A2 (no Helm), or open a Helm ADR | repository owner | **pending** |
| Amend the P1-37/P1-39 staging path (an approved design decision; P1-38:18) | repository owner (author of those records) | pending; depends on this ADR; proposed text in the appendix |
| kubeconform policy for `kustomization.yaml` and CRD kinds | owner / SRE (`/infra/` code owner) | pending |
| A5 (staging manifests) and every later gate | owner, per P1-38 §2 | **unapproved**; this ADR does not approve any of them |

## Appendix: proposed amendment text (applies only if Option 1 is accepted)

**How to amend.** The owner records the change as an amendment, for example a
short record "P1-37 amendment 1 (ADR-0024)". The original lines are not edited,
so that history keeps what was approved and when. Lines that describe the
repository's state at the time are history, and stay as they are: P1-33:22,
P1-34A:5, P1-34B:8, P1-35:7, P1-36:5, P1-37:14 and P1-41:18.

| Record and line | Now | Becomes |
|---|---|---|
| P1-37:59 (§1.6, approved design) | Staging manifests go in `infra/kubernetes/overlays/staging/`. | Staging manifests go in `infra/k8s/overlays/staging/` (ADR-0024). |
| P1-37:60-61 | `infra/k8s/base/services/` is left untouched; `deployment.yaml` is never restored. | unchanged |
| P1-37:95 (gate A5) | the staging set under `infra/kubernetes/overlays/staging/` | the staging set under `infra/k8s/overlays/staging/` |
| P1-38:18 | "…and the staging manifest path `infra/kubernetes/overlays/staging/`" | "…`infra/k8s/overlays/staging/` (amended by ADR-0024)" |
| P1-38:118, P1-38:142 (O-4 recommendation; gate A5) | `infra/kubernetes/overlays/staging/` | `infra/k8s/overlays/staging/` |
| P1-39:24 (O-1) | "Staging manifests only under `infra/kubernetes/overlays/staging/`" | "…only under `infra/k8s/overlays/staging/`". Every other O-1 term is unchanged. |
| P1-39:31 (O-4, conditional) | one shared ConfigMap in `infra/kubernetes/overlays/staging/` | in `infra/k8s/overlays/staging/`. Still conditional. |
| P1-41:68 (tracked) | names `infra/kubernetes/overlays/staging/` | no edit; a successor record or erratum notes the new path |
| `docker/compose/docker-compose.yml:8` (tracked) | "Production runs on Kubernetes from infra/kubernetes." | "…from infra/k8s." |

## References

- ADR-0008 (cloud infrastructure)
- `docker/compose/docker-compose.yml`
- `docs/telephony/P1-41_INFRA_AUTH_READINESS_AUDIT.md`
- the owner's untracked `infra/README.md`, P1-37, P1-38 and P1-39 (not on
  `main`)
- supersedes: none
