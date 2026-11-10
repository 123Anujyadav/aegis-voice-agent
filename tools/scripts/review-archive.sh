#!/usr/bin/env bash
# =============================================================================
# review-archive.sh — build the source archive for an independent security
# review (OD-12) at ONE fixed commit of main, containing only what the scope
# needs. docs/phase19/OD-12-REVIEWER-SHORTLIST.md §6.
#
# Usage:
#   tools/scripts/review-archive.sh <commit> <out-dir> [--verify]
#
# What it does, and nothing else:
#   * Refuses a commit that is not on origin/main: the owner's local branches
#     and uncommitted work can never reach a reviewer through this script.
#   * Archives a FIXED ALLOWLIST of paths (scope "batch-a" below) with
#     `git archive`, so the content is exactly the committed tree at <commit>.
#   * Checks the Go module set is closed: every local `replace => ../x` in an
#     archived go.mod resolves to a directory inside the archive, so the
#     reviewer can build with GOWORK=off and nothing else from the repository.
#   * Writes <out-dir>/MANIFEST.txt (commit, scope, paths, file count) and
#     <out-dir>/SHA256SUMS for the archive.
#   * With --verify: extracts the archive into a temporary directory and runs
#     `go build`, `go vet` and `go test -short` on the identity and edge-api
#     modules there, offline from the repository (Go may fetch public
#     third-party modules listed in go.sum).
#
# It sends nothing anywhere. Sharing the archive is the owner's decision, after
# a confidentiality agreement (OWNER-ACTION-PACK-2026-11-10.md §3).
# =============================================================================
set -euo pipefail

if [[ $# -lt 2 || $# -gt 3 ]]; then
  echo "usage: $0 <commit> <out-dir> [--verify]" >&2
  exit 2
fi
commit_arg=$1
out_dir=$2
verify=${3:-}
if [[ -n "$verify" && "$verify" != "--verify" ]]; then
  echo "unknown option: $verify" >&2
  exit 2
fi

repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root"

commit=$(git rev-parse --verify --quiet "${commit_arg}^{commit}") || {
  echo "not a commit: $commit_arg" >&2
  exit 1
}
git fetch --quiet origin main
if ! git merge-base --is-ancestor "$commit" origin/main; then
  echo "refusing: $commit is not on origin/main" >&2
  exit 1
fi
short=$(git rev-parse --short=12 "$commit")

# Scope batch-a: the OTP review (OD-12-OTP-REVIEW-PACKAGE.md §2, S1–S7). The
# identity service and the shared packages it builds against; edge-api, which
# the real-PostgreSQL evidence script also runs; that script and its workflow;
# and the decision records the reviewer reads.
paths=(
  services/go/identity
  services/go/edge-api
  packages/go/accesstoken
  packages/go/dpop
  packages/go/metrics
  packages/go/metricsexport
  packages/go/middleware
  packages/go/pgshared
  packages/go/platform
  packages/go/redis
  packages/go/runtime
  packages/go/telemetry
  tools/scripts/identity-postgres-tests.sh
  .github/workflows/identity-postgres.yml
  docs/adr/0010-authentication-and-device-trust.md
  docs/adr/0033-otp-ownership-and-delivery-only-sms-providers.md
  docs/phase19/OD-12-OTP-REVIEW-PACKAGE.md
  docs/phase19/OTP-PROVIDERS.md
  docs/runbooks/identity-provider-onboarding.md
  SECURITY.md
)

for p in "${paths[@]}"; do
  if [[ -z "$(git ls-tree --name-only "$commit" -- "$p")" ]]; then
    echo "path not in $short: $p" >&2
    exit 1
  fi
done

# Closure: every local replace in an archived go.mod points inside the archive.
in_scope() {
  local want=$1 p
  for p in "${paths[@]}"; do
    [[ "$want" == "$p" ]] && return 0
  done
  return 1
}
missing=0
for p in "${paths[@]}"; do
  gomod="$p/go.mod"
  git cat-file -e "$commit:$gomod" 2>/dev/null || continue
  while read -r rel; do
    target=$(realpath -m --relative-to="$repo_root" "$repo_root/$p/$rel")
    if ! in_scope "$target"; then
      echo "module closure broken: $gomod replaces => $rel ($target), which is not archived" >&2
      missing=1
    fi
  done < <(git show "$commit:$gomod" | sed -n 's/^replace .* => \(\.\.[^ ]*\).*$/\1/p')
done
if [[ $missing -ne 0 ]]; then
  exit 1
fi

mkdir -p "$out_dir"
out_dir=$(cd "$out_dir" && pwd)
name="lily-ai-review-batch-a-$short"
archive="$out_dir/$name.tar.gz"

# gzip -n: no timestamp or name in the header, so the same commit always
# produces the same bytes and the same checksum.
git archive --format=tar --prefix="$name/" "$commit" -- "${paths[@]}" | gzip -n -9 > "$archive"

files=$(tar -tzf "$archive" | grep -cv '/$')
{
  echo "commit: $commit"
  echo "commit date: $(git show -s --format=%cI "$commit")"
  echo "scope: batch-a (OD-12 OTP review, S1-S7)"
  echo "archive: $(basename "$archive")"
  echo "files: $files"
  echo "paths:"
  printf '  %s\n' "${paths[@]}"
} > "$out_dir/MANIFEST.txt"
(cd "$out_dir" && sha256sum "$(basename "$archive")" > SHA256SUMS)

echo "archive:  $archive"
echo "files:    $files"
echo "sha256:   $(cut -d' ' -f1 "$out_dir/SHA256SUMS")"

if [[ "$verify" == "--verify" ]]; then
  scratch=$(mktemp -d)
  trap 'rm -rf "$scratch"' EXIT
  tar -xzf "$archive" -C "$scratch"
  for m in services/go/identity services/go/edge-api; do
    echo "verify: $m"
    (cd "$scratch/$name/$m" && GOWORK=off go build ./... && GOWORK=off go vet ./... && GOWORK=off go test -short -count=1 ./... >/dev/null)
  done
  echo "verify: ok (built, vetted and tested from the archive alone)"
fi
