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
  go test -count=1 -v "${GO_TEST_EXTRA[@]}" ./...) > "$log" 2>&1 || status=$?

grep -E '^(ok|FAIL|---)|^\s+--- (FAIL|SKIP)|panic:|WARNING: DATA RACE' "$log" | grep -v -- '--- PASS' || true

passes="$(grep -cE -- '--- PASS' "$log" || true)"
fails="$(grep -cE -- '--- FAIL' "$log" || true)"
skips="$(grep -cE -- '--- SKIP' "$log" || true)"
echo "== tests (including subtests): pass=$passes fail=$fails skip=$skips"

if grep -qF -- "$pass" "$log"; then
  echo "the throwaway database password appeared in the test output" >&2
  status=1
fi
if [ "$skips" -ne 0 ]; then
  echo "== skipped tests (each one is a test that did not run):" >&2
  grep -E -- '--- SKIP' "$log" >&2 || true
  echo "a test skipped: with a database provided, every test must run" >&2
  status=1
fi
if [ "$status" -ne 0 ]; then
  echo "== failure output (tail):" >&2
  tail -n 80 "$log" >&2
fi
exit "$status"
