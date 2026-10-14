#!/usr/bin/env bash
# =============================================================================
# telephony-integration.sh — run the real-PostgreSQL and real-Redis integration
# suites (packages/go/telephonystore, packages/go/telephonyredis) against
# THROWAWAY local containers. Phase 19 P1-15.
#
# Opt-in and local only:
#   * Images are used ONLY if already present locally (--pull never). Nothing
#     is downloaded. A missing image skips that suite, loudly.
#   * Each container binds to 127.0.0.1 on a Docker-chosen port, uses a random
#     one-off password generated here, and is removed on exit (trap), whatever
#     happens.
#   * The DSN / address are passed to `go test` through the environment only;
#     the password is never echoed.
#   * It never connects to anything it did not start.
#
# Usage (from the repository root):
#   tools/scripts/telephony-integration.sh            # both suites
#   REDIS_IMAGE=docker.io/library/redis@sha256:02419de7eddf55aa5bcf49efb74e88fa8d931b4d77c07eff8a6b2144472b6952 \
#     tools/scripts/telephony-integration.sh
#   (the official redis:7.4.2-alpine index, pinned by digest and verified in
#   P1-16; pull it once with --platform linux/amd64 — this script never pulls)
#   SKIP_POSTGRES=1 ... runs the Redis suite alone.
#
# Residual, local only: the throwaway Redis password is a redis-server argument
# inside the container, so `docker inspect` / `ps` on this machine can show it
# while the container lives. It is random, single-use and bound to 127.0.0.1.
#
# Exit status is non-zero if any suite that RAN failed.
# =============================================================================
set -euo pipefail

POSTGRES_IMAGE="${POSTGRES_IMAGE:-postgres:17.2-alpine}"
REDIS_IMAGE="${REDIS_IMAGE:-}"
RUN_ID="cs-p115-$(od -An -N4 -tx4 /dev/urandom | tr -d ' \n')"
GO_ENV=(GOFLAGS=-mod=readonly GOTOOLCHAIN=local GOPROXY=off)
# Extra `go test` flags, e.g. GO_TEST_FLAGS=-race (needs cgo and a C compiler).
read -r -a GO_TEST_EXTRA <<< "${GO_TEST_FLAGS:-}"
# REQUIRE_ALL=1 (CI): a suite that cannot run is a failure, never a skip.
REQUIRE_ALL="${REQUIRE_ALL:-}"
skipped() {
  echo "== $1"
  if [ "$REQUIRE_ALL" = "1" ]; then echo "REQUIRE_ALL=1: a suite did not run" >&2; STATUS=1; fi
}
CONTAINERS=()
STATUS=0

cleanup() {
  for c in "${CONTAINERS[@]:-}"; do
    [ -n "$c" ] && docker rm -f "$c" >/dev/null 2>&1 || true
  done
}
trap cleanup EXIT INT TERM

have_image() { docker image inspect "$1" >/dev/null 2>&1; }
random_secret() { od -An -N24 -tx1 /dev/urandom | tr -d ' \n'; }
host_port() { docker port "$1" "$2" | head -n1 | sed 's/.*://'; }

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"

# --- PostgreSQL -----------------------------------------------------------------
if [ "${SKIP_POSTGRES:-}" = "1" ]; then
  skipped "PostgreSQL suite SKIPPED: SKIP_POSTGRES=1"
elif have_image "$POSTGRES_IMAGE"; then
  pg_name="${RUN_ID}-pg"
  pg_pass="$(random_secret)"
  docker run -d --rm --pull never --name "$pg_name" \
    -e POSTGRES_PASSWORD="$pg_pass" -p 127.0.0.1::5432 "$POSTGRES_IMAGE" >/dev/null
  CONTAINERS+=("$pg_name")

  ready=0
  for _ in $(seq 1 120); do
    if docker exec "$pg_name" psql -U postgres -tAc 'SELECT 1' >/dev/null 2>&1; then
      ready=1; break
    fi
    sleep 0.5
  done
  if [ "$ready" -ne 1 ]; then
    echo "postgres container did not become ready" >&2; exit 1
  fi
  pg_port="$(host_port "$pg_name" 5432/tcp)"
  echo "== PostgreSQL suite against throwaway $POSTGRES_IMAGE on 127.0.0.1:$pg_port"
  if ! (cd "$repo_root/packages/go/telephonystore" && \
        env "${GO_ENV[@]}" \
          CS_TELEPHONYSTORE_TEST_DSN="postgres://postgres:${pg_pass}@127.0.0.1:${pg_port}/postgres?sslmode=disable" \
          go test -count=1 -v "${GO_TEST_EXTRA[@]}" -run 'TestPostgres' ./...); then
    STATUS=1
  fi
else
  skipped "PostgreSQL suite SKIPPED: image $POSTGRES_IMAGE is not present locally (not pulled)"
fi

# --- Redis ----------------------------------------------------------------------
if [ -n "$REDIS_IMAGE" ] && have_image "$REDIS_IMAGE"; then
  rd_name="${RUN_ID}-redis"
  rd_pass="$(random_secret)"
  docker run -d --rm --pull never --name "$rd_name" -p 127.0.0.1::6379 \
    "$REDIS_IMAGE" redis-server --requirepass "$rd_pass" --save '' --appendonly no >/dev/null
  CONTAINERS+=("$rd_name")

  ready=0
  for _ in $(seq 1 60); do
    # Password via the environment (REDISCLI_AUTH), not a command-line flag.
    if docker exec -e REDISCLI_AUTH="$rd_pass" "$rd_name" redis-cli PING 2>/dev/null | grep -q PONG; then
      ready=1; break
    fi
    sleep 0.5
  done
  if [ "$ready" -ne 1 ]; then
    echo "redis container did not become ready" >&2; exit 1
  fi
  rd_port="$(host_port "$rd_name" 6379/tcp)"
  echo "== Redis suite against throwaway $REDIS_IMAGE on 127.0.0.1:$rd_port"
  if ! (cd "$repo_root/packages/go/telephonyredis" && \
        env "${GO_ENV[@]}" \
          CS_TELEPHONYREDIS_TEST_ADDR="127.0.0.1:${rd_port}" \
          CS_TELEPHONYREDIS_TEST_PASSWORD="$rd_pass" \
          go test -count=1 -v "${GO_TEST_EXTRA[@]}" -run 'TestRedis' ./...); then
    STATUS=1
  fi
  # Server-side evidence, read-only: the server version, the commands it
  # executed (proving the suite reached THIS instance), and the remaining key
  # count (0 = every test cleaned up its own prefix). No secret is printed.
  echo "== Redis server evidence"
  docker exec -e REDISCLI_AUTH="$rd_pass" "$rd_name" redis-cli INFO server \
    | grep -E '^(redis_version|redis_mode|os|arch_bits):' | tr -d '\r'
  docker exec -e REDISCLI_AUTH="$rd_pass" "$rd_name" redis-cli INFO commandstats \
    | grep -E '^cmdstat_(eval|hset|hget|set|exists|scan|del|lrange|lpush|client[|][a-z]+|blpop|ping|auth):' \
    | cut -d, -f1 | tr -d '\r'
  echo "dbsize_after_suite=$(docker exec -e REDISCLI_AUTH="$rd_pass" "$rd_name" redis-cli DBSIZE | tr -d '\r')"
else
  skipped "Redis suite SKIPPED: set REDIS_IMAGE to a locally present Redis image (nothing is pulled)"
fi

exit "$STATUS"
