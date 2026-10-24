#!/usr/bin/env bash
# =============================================================================
# android-identity-contract.sh — the Android identity and edge-api clients
# (Kotlin, JVM) against the REAL identity and edge-api services (Go), end to
# end.
#
# The Android client's own tests use MockWebServer, and identity's tests use
# Go clients. Neither proves that the two agree. This runs identity as a real
# process, on a real PostgreSQL, with its real HTTP API and DPoP verifier, and
# runs IdentityLiveContractTest (android/core/network) against it:
# enrolment, refresh, device binding and refresh-token reuse. It then runs the
# real edge-api binary on the same database (identity's migrations create its
# DPoP replay table) and EdgeLiveContractTest (android/core/auth): the app's
# authenticator signs in, and EdgeApiClient calls GET /v1/me with the
# identity-issued token and this device's proof; another key's proof is
# refused.
#
# What is test-only:
#   * identity runs as its TestE2EIdentityProcess helper
#     (services/go/identity/cmd/server/e2e_process_test.go): the configured
#     service through configureIdentity and platform.Service, plus ONE
#     addition, an OTP sender that prints each code to stdout (production has
#     no OTP delivery). stdout goes to a file the Kotlin test reads.
#   * Keys and the database are generated here, for this run only: an Ed25519
#     signing key, an HMAC key, and a throwaway PostgreSQL container on
#     127.0.0.1 with a random password. All are deleted on exit.
#   * identity serves plain HTTP, as it does behind production's load
#     balancer; a TLS-terminating proxy (tools/scripts/tlsproxy) fronts it
#     on loopback, with a CA and certificate made for this run. The client
#     signs DPoP proofs only for https URLs, so the canonical proof URLs and
#     the client's origin are the proxy's https://127.0.0.1:<port>.
#
# Disposable by construction, as identity-postgres-tests.sh: the image must
# already be present (--pull never), and the run fails unless the Kotlin
# test's JUnit report shows it ran with nothing skipped.
#
# Requires: docker, go, openssl, python3, a JDK for Gradle.
# Usage: POSTGRES_IMAGE=<image@digest> bash tools/scripts/android-identity-contract.sh
# =============================================================================
set -euo pipefail

POSTGRES_IMAGE="${POSTGRES_IMAGE:?set POSTGRES_IMAGE to a locally present image, pinned by digest}"
repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
name="cs-idcontract-$(od -An -N4 -tx4 /dev/urandom | tr -d ' \n')"
work="$(mktemp -d)"
started=0
identity_pid=""
proxy_pid=""
edge_pid=""
edge_proxy_pid=""

cleanup() {
  for pid in "$proxy_pid" "$edge_proxy_pid" "$edge_pid"; do
    if [ -n "$pid" ]; then kill "$pid" 2>/dev/null || true; fi
  done
  # Closing identity's stdin begins its graceful shutdown (the helper's
  # portable stand-in for SIGTERM); wait briefly, then make sure.
  exec 3>&- || true
  if [ -n "$identity_pid" ]; then
    for _ in $(seq 1 50); do kill -0 "$identity_pid" 2>/dev/null || break; sleep 0.1; done
    kill "$identity_pid" 2>/dev/null || true
  fi
  if [ "$started" = "1" ]; then
    docker rm -f "$name" >/dev/null 2>&1 || true
  fi
  rm -rf "$work"
}
trap cleanup EXIT INT TERM

if ! docker image inspect "$POSTGRES_IMAGE" >/dev/null 2>&1; then
  echo "image $POSTGRES_IMAGE is not present locally (this script never pulls)" >&2
  exit 1
fi

free_port() { python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()'; }

# --- PostgreSQL, throwaway ----------------------------------------------------
pass="$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')"
if [ "${GITHUB_ACTIONS:-}" = "true" ]; then
  echo "::add-mask::$pass"
fi
docker run -d --rm --pull never --name "$name" \
  -e POSTGRES_PASSWORD="$pass" -p 127.0.0.1::5432 "$POSTGRES_IMAGE" >/dev/null
started=1
ready=0
for _ in $(seq 1 120); do
  if docker exec "$name" pg_isready -h 127.0.0.1 -U postgres -q >/dev/null 2>&1; then ready=1; break; fi
  sleep 0.5
done
[ "$ready" = "1" ] || { echo "the throwaway PostgreSQL container did not become ready" >&2; exit 1; }
pg_port="$(docker port "$name" 5432/tcp | head -n1 | sed 's/.*://')"
printf 'postgres://postgres:%s@127.0.0.1:%s/postgres?sslmode=disable\n' "$pass" "$pg_port" > "$work/dsn"

# --- Keys, generated for this run --------------------------------------------
openssl genpkey -algorithm ed25519 -out "$work/signing.pem" 2>/dev/null
head -c 48 /dev/urandom > "$work/hmac.key"
# TLS for the loopback proxy: a CA and a server certificate for IP:127.0.0.1,
# trusted only by this run's test (AEGIS_IDENTITY_E2E_CA_FILE).
(
  cd "$work"
  openssl ecparam -name prime256v1 -genkey -noout -out ca.key 2>/dev/null
  openssl req -x509 -new -key ca.key -sha256 -days 1 -subj "/CN=aegis-android-contract-ca" \
    -addext "basicConstraints=critical,CA:TRUE" -addext "keyUsage=critical,keyCertSign,cRLSign" -out ca.crt 2>/dev/null
  openssl ecparam -name prime256v1 -genkey -noout -out tls.key 2>/dev/null
  openssl req -new -key tls.key -subj "/CN=127.0.0.1" -out tls.csr 2>/dev/null
  printf 'subjectAltName=IP:127.0.0.1\nbasicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature\nextendedKeyUsage=serverAuth\n' > tls.ext
  openssl x509 -req -in tls.csr -CA ca.crt -CAkey ca.key -CAcreateserial -days 1 -sha256 -extfile tls.ext -out tls.crt 2>/dev/null
  rm -f ca.key tls.csr tls.ext ca.srl
) || { echo "generating the throwaway TLS certificates failed" >&2; exit 1; }

# --- identity: build, migrate, serve -----------------------------------------
echo "== building identity's test binary"
(cd "$repo_root/services/go/identity" && go test -c -o "$work/identity-e2e.test" ./cmd/server)

# The helper's fixed arguments, and the environment every mode shares.
# CS_IDENTITY_ENVIRONMENT=development: the throwaway database is plaintext,
# which identity otherwise refuses (ADR-0029).
identity_bin="$work/identity-e2e.test"
identity_args=('-test.run=^TestE2EIdentityProcess$' -test.count=1 -test.timeout=10m)
identity_env=(CS_IDENTITY_ENVIRONMENT=development "CS_IDENTITY_DATABASE_DSN_FILE=$work/dsn")

echo "== migrating"
env "${identity_env[@]}" AEGIS_E2E_IDENTITY_PROCESS=migrate "$identity_bin" "${identity_args[@]}" \
  > "$work/migrate.log" 2>&1 || { cat "$work/migrate.log"; echo "identity migrate failed" >&2; exit 1; }
grep -q PASS "$work/migrate.log" || { cat "$work/migrate.log"; echo "identity migrate did not pass" >&2; exit 1; }

http_port="$(free_port)"
health_port="$(free_port)"
tls_port="$(free_port)"
# The public origin is the TLS proxy's, as in production behind a load
# balancer: the client signs proofs only for https URLs.
origin="https://127.0.0.1:$tls_port"
mkfifo "$work/stdin"
# Started through env, so $! is identity itself, not a subshell.
env "${identity_env[@]}" AEGIS_E2E_IDENTITY_PROCESS=serve \
  CS_IDENTITY_HTTP_PORT="$http_port" \
  CS_IDENTITY_HEALTH_PORT="$health_port" \
  CS_IDENTITY_SHUTDOWN_TIMEOUT=3s \
  CS_IDENTITY_ACCESS_TOKEN_PRIVATE_KEY_FILE="$work/signing.pem" \
  CS_IDENTITY_ACCESS_TOKEN_ISSUER=urn:callscreen:identity:android-contract \
  CS_IDENTITY_ACCESS_TOKEN_AUDIENCE=urn:callscreen:edge-api:android-contract \
  CS_IDENTITY_ACCESS_TOKEN_KID=android-contract \
  CS_IDENTITY_ENROLLMENT_CANONICAL_URL="$origin/v1/otp/validate" \
  CS_IDENTITY_REFRESH_CANONICAL_URL="$origin/v1/token/refresh" \
  CS_IDENTITY_HMAC_SECRET_KEY_FILE="$work/hmac.key" \
  "$identity_bin" "${identity_args[@]}" \
  < "$work/stdin" > "$work/otp.log" 2> "$work/identity.log" &
identity_pid=$!
exec 3> "$work/stdin" # hold identity's stdin open; closing it shuts identity down

ready=0
for _ in $(seq 1 120); do
  if curl -fsS -o /dev/null "http://127.0.0.1:$health_port/readyz" 2>/dev/null; then ready=1; break; fi
  kill -0 "$identity_pid" 2>/dev/null || break
  sleep 0.5
done
if [ "$ready" != "1" ]; then
  echo "identity did not become ready; its log:" >&2
  cat "$work/identity.log" >&2
  exit 1
fi
echo "== identity ready on http://127.0.0.1:$http_port"

# --- TLS in front of identity ------------------------------------------------
(cd "$repo_root/tools/scripts/tlsproxy" && GOWORK=off go build -o "$work/tlsproxy" main.go)
"$work/tlsproxy" -listen "127.0.0.1:$tls_port" -upstream "http://127.0.0.1:$http_port" \
  -cert "$work/tls.crt" -key "$work/tls.key" 2> "$work/tlsproxy.log" &
proxy_pid=$!
ready=0
for _ in $(seq 1 60); do
  # Any HTTP answer through TLS proves the proxy and the certificate work.
  if curl -sS --cacert "$work/ca.crt" -o /dev/null "$origin/healthz" 2>/dev/null; then ready=1; break; fi
  kill -0 "$proxy_pid" 2>/dev/null || break
  sleep 0.5
done
if [ "$ready" != "1" ]; then
  echo "the TLS proxy did not come up; its log:" >&2
  cat "$work/tlsproxy.log" >&2
  exit 1
fi
echo "== identity reachable on $origin"

# --- edge-api: the real binary, on identity's database, behind TLS ------------
# It trusts identity's signing key (the public half of this run's key), and
# its replay table is created by identity's migrations, which ran above.
echo "== building edge-api"
(cd "$repo_root/services/go/edge-api" && go build -o "$work/edge-api" ./cmd/server)
openssl pkey -in "$work/signing.pem" -pubout -out "$work/signing.pub.pem" 2>/dev/null
edge_http="$(free_port)"
edge_health="$(free_port)"
edge_tls="$(free_port)"
edge_origin="https://127.0.0.1:$edge_tls"
env CS_EDGE_API_ENVIRONMENT=development \
  CS_EDGE_API_HTTP_PORT="$edge_http" \
  CS_EDGE_API_HEALTH_PORT="$edge_health" \
  CS_EDGE_API_SHUTDOWN_TIMEOUT=3s \
  CS_EDGE_API_ACCESS_TOKEN_PUBLIC_KEY_FILE="$work/signing.pub.pem" \
  CS_EDGE_API_ACCESS_TOKEN_KID=android-contract \
  CS_EDGE_API_ACCESS_TOKEN_ISSUER=urn:callscreen:identity:android-contract \
  CS_EDGE_API_ACCESS_TOKEN_AUDIENCE=urn:callscreen:edge-api:android-contract \
  CS_EDGE_API_PUBLIC_ORIGIN="$edge_origin" \
  CS_EDGE_API_DATABASE_DSN_FILE="$work/dsn" \
  "$work/edge-api" > "$work/edge-api.log" 2>&1 &
edge_pid=$!
ready=0
for _ in $(seq 1 120); do
  if curl -fsS -o /dev/null "http://127.0.0.1:$edge_health/readyz" 2>/dev/null; then ready=1; break; fi
  kill -0 "$edge_pid" 2>/dev/null || break
  sleep 0.5
done
if [ "$ready" != "1" ]; then
  echo "edge-api did not become ready; its log:" >&2
  cat "$work/edge-api.log" >&2
  exit 1
fi
"$work/tlsproxy" -listen "127.0.0.1:$edge_tls" -upstream "http://127.0.0.1:$edge_http" \
  -cert "$work/tls.crt" -key "$work/tls.key" 2> "$work/tlsproxy-edge.log" &
edge_proxy_pid=$!
ready=0
for _ in $(seq 1 60); do
  if curl -sS --cacert "$work/ca.crt" -o /dev/null "$edge_origin/healthz" 2>/dev/null; then ready=1; break; fi
  kill -0 "$edge_proxy_pid" 2>/dev/null || break
  sleep 0.5
done
if [ "$ready" != "1" ]; then
  echo "edge-api's TLS proxy did not come up; its log:" >&2
  cat "$work/tlsproxy-edge.log" >&2
  exit 1
fi
echo "== edge-api reachable on $edge_origin"

# --- The Android client against them ----------------------------------------
# One suite per Gradle run (the build runs projects in parallel, and both
# suites read the same OTP file). --rerun: the result depends on the live
# servers, never on Gradle's cache.
run_suite() {
  local task="$1" suite="$2" report="$3" status=0
  (cd "$repo_root/android" && \
    AEGIS_IDENTITY_E2E_BASE_URL="$origin" AEGIS_IDENTITY_E2E_OTP_FILE="$work/otp.log" \
    AEGIS_IDENTITY_E2E_CA_FILE="$work/ca.crt" AEGIS_EDGE_E2E_ORIGIN="$edge_origin" \
    ./gradlew --no-daemon --stacktrace "$task" --tests "$suite" --rerun) || status=$?
  if [ "$status" != "0" ]; then
    echo "== identity's log (the run failed):" >&2
    cat "$work/identity.log" >&2
    echo "== edge-api's log:" >&2
    cat "$work/edge-api.log" >&2
    exit "$status"
  fi
  local name="${suite##*.}"
  grep -Eq '<testsuite [^>]*tests="[1-9][0-9]*"' "$report" || { echo "$name did not run ($report)" >&2; exit 1; }
  grep -Eq '<testsuite [^>]*skipped="0"' "$report" || { echo "$name was skipped ($report)" >&2; exit 1; }
  grep -Eq '<testsuite [^>]*failures="0"[^>]*errors="0"' "$report" || { echo "$name failed ($report)" >&2; exit 1; }
  # The evidence, from the JUnit report itself: the counts and each test case.
  echo "== $name JUnit report: $(grep -oE '<testsuite [^>]*(tests|skipped|failures|errors)="[0-9]+"[^>]*' "$report" | grep -oE '(tests|skipped|failures|errors)="[0-9]+"' | tr '\n' ' ')"
  grep -oE '<testcase name="[^"]*"' "$report" | sed "s/<testcase name=/== $name test case: /"
  if grep -q "$pass" "$report"; then
    echo "the database password appeared in the output" >&2
    exit 1
  fi
  echo "== $name passed"
}

results="$repo_root/android/core/%s/build/test-results/testDebugUnitTest/TEST-%s.xml"
identity_suite=com.callscreen.core.network.identity.IdentityLiveContractTest
edge_suite=com.callscreen.core.auth.EdgeLiveContractTest
# shellcheck disable=SC2059 # results is this script's own format string
run_suite :core:network:testDebugUnitTest "$identity_suite" "$(printf "$results" network "$identity_suite")"
# shellcheck disable=SC2059
run_suite :core:auth:testDebugUnitTest "$edge_suite" "$(printf "$results" auth "$edge_suite")"
if grep -q "$pass" "$work/identity.log" "$work/edge-api.log"; then
  echo "the database password appeared in a service log" >&2
  exit 1
fi
echo "== the Android client passed against the real identity and edge-api services"
