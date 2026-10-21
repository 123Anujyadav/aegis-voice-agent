#!/usr/bin/env bash
# =============================================================================
# gateway-postgres-tests.sh — run the telephony-gateway test suite WITH its
# real-PostgreSQL tests, against a THROWAWAY local container (D8; P1-49 §3.3).
#
# Without CS_TELEPHONY_GATEWAY_FENCE_TEST_DSN, those tests SKIP. pr-go sets no
# DSN, so in CI they have never run. This script gives them a disposable
# database and turns any remaining skip into a failure.
#
# Disposable by construction:
#   * The image must already be present locally and is used with --pull never.
#     This script never downloads anything; the workflow pulls one image pinned
#     by digest before calling it.
#   * The container binds to 127.0.0.1 on a Docker-chosen port, uses a random
#     one-off password generated here, and is removed on exit (trap), whatever
#     happens.
#   * The suites CREATE/ALTER/DROP ROLE and call pg_terminate_backend, so they
#     need a superuser. That is acceptable ONLY on an instance like this one,
#     started for the run and destroyed after it. Never point this at a shared
#     or long-lived database.
#   * The DSN reaches `go test` through the environment only. The password is
#     never echoed, is masked in GitHub Actions, and the run fails if it ever
#     appears in the test output.
#
# Fails (non-zero) if the image is missing, the database does not become
# ready, any test fails, or ANY test skips: a skip here would mean a
# real-PostgreSQL test silently did not run.
#
# Usage (from the repository root):
#   POSTGRES_IMAGE=docker.io/library/postgres@sha256:<digest> \
#   GO_TEST_FLAGS=-race \
#     tools/scripts/gateway-postgres-tests.sh
# =============================================================================
set -euo pipefail

POSTGRES_IMAGE="${POSTGRES_IMAGE:?set POSTGRES_IMAGE to a locally present image, pinned by digest}"
# Extra `go test` flags, e.g. GO_TEST_FLAGS=-race (needs cgo and a C compiler).
read -r -a GO_TEST_EXTRA <<< "${GO_TEST_FLAGS:-}"

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
module="$repo_root/services/go/telephony-gateway"
name="cs-gwpg-$(od -An -N4 -tx4 /dev/urandom | tr -d ' \n')"
log="$(mktemp)"
started=0

cleanup() {
  if [ "$started" = "1" ]; then
    docker rm -f "$name" >/dev/null 2>&1 || true
  fi
  rm -f "$log"
}
trap cleanup EXIT INT TERM

if ! docker image inspect "$POSTGRES_IMAGE" >/dev/null 2>&1; then
  echo "image $POSTGRES_IMAGE is not present locally (this script never pulls)" >&2
  exit 1
fi

pass="$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')"
if [ "${GITHUB_ACTIONS:-}" = "true" ]; then
  echo "::add-mask::$pass"
fi

docker run -d --rm --pull never --name "$name" \
  -e POSTGRES_PASSWORD="$pass" -p 127.0.0.1::5432 "$POSTGRES_IMAGE" >/dev/null
started=1

# pg_isready over TCP, not the Unix socket: the image's first-boot init server
# listens on the socket only, then restarts. A TCP answer means the real
# server is up.
ready=0
for _ in $(seq 1 120); do
  if docker exec "$name" pg_isready -h 127.0.0.1 -U postgres -q >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 0.5
done
if [ "$ready" -ne 1 ]; then
  echo "the throwaway PostgreSQL container did not become ready" >&2
  exit 1
fi
port="$(docker port "$name" 5432/tcp | head -n1 | sed 's/.*://')"
echo "== telephony-gateway suite against throwaway $POSTGRES_IMAGE on 127.0.0.1:$port"
echo "== server: $(docker exec "$name" psql -U postgres -tAc 'SHOW server_version')"

status=0
(cd "$module" && \
  CS_TELEPHONY_GATEWAY_FENCE_TEST_DSN="postgres://postgres:${pass}@127.0.0.1:${port}/postgres?sslmode=disable" \
  go test -count=1 -json "${GO_TEST_EXTRA[@]}" ./...) > "$log" 2>&1 || status=$?

# The password check comes first: if it appears anywhere, no output is echoed.
if grep -qF -- "$pass" "$log"; then
  echo "the throwaway database password appeared in the test output; output withheld" >&2
  exit 1
fi

# go test -json attributes every output line to its test, so a failing test's
# complete output (its t.Log/t.Error lines, a race report, a panic) can be
# printed even when parallel tests interleave. Non-JSON lines, such as build
# errors, are printed as they are.
py=""
for candidate in python3 python; do
  # A name can resolve to a stub that cannot run (the Windows Store alias),
  # so each candidate must actually execute.
  if "$candidate" -c "import sys" >/dev/null 2>&1; then py="$candidate"; break; fi
done
if [ -z "$py" ]; then
  echo "no working python3 or python to summarise the test results" >&2
  exit 1
fi
summary_status=0
"$py" -I - "$log" <<'PY' || summary_status=$?
import json
import sys

counts = {"pass": 0, "fail": 0, "skip": 0}
outputs, failed, skipped, packages, raw = {}, [], [], [], []
with open(sys.argv[1], encoding="utf-8", errors="replace") as fh:
    for line in fh:
        if not line.startswith("{"):
            raw.append(line.rstrip("\n"))
            continue
        e = json.loads(line)
        key = (e.get("Package", ""), e.get("Test", ""))
        action = e.get("Action")
        if action == "output":
            outputs.setdefault(key, []).append(e.get("Output", ""))
        elif action in counts and key[1]:
            counts[action] += 1
            (failed if action == "fail" else skipped if action == "skip" else []).append(key)
        elif action in ("pass", "fail", "skip") and not key[1]:
            packages.append((action, key[0]))

for action, pkg in packages:
    print(("ok  " if action == "pass" else "FAIL" if action == "fail" else "skip") + "\t" + pkg)
print("== tests (including subtests): pass={pass} fail={fail} skip={skip}".format(**counts))
for pkg, test in skipped:
    print(f"== skipped (did not run): {pkg} {test}", file=sys.stderr)
for pkg, test in failed:
    print(f"== output of failing test {test} ({pkg}):", file=sys.stderr)
    sys.stderr.write("".join(outputs.get((pkg, test), [])))
for action, pkg in packages:
    if action == "fail" and not any(p == pkg for p, _ in failed):
        print(f"== {pkg} failed outside any test (build, TestMain, timeout or panic):", file=sys.stderr)
        sys.stderr.write("".join(outputs.get((pkg, ""), [])))
if raw and (failed or any(a == "fail" for a, _ in packages) or not packages):
    print("== non-JSON output (for example build errors):", file=sys.stderr)
    print("\n".join(raw), file=sys.stderr)
sys.exit(2 if counts["skip"] else 0)
PY

if [ "$summary_status" -eq 2 ]; then
  echo "a test skipped: with a database provided, every test must run" >&2
  status=1
elif [ "$summary_status" -ne 0 ]; then
  echo "could not summarise the test output (exit $summary_status)" >&2
  status=1
fi
exit "$status"
