#!/usr/bin/env bash
# =============================================================================
# identity-postgres-tests.sh — run the identity and edge-api test suites WITH
# their real-PostgreSQL tests, against a THROWAWAY local container (ADR-0029).
#
# Without AEGIS_IDENTITY_PG_DSN, those tests SKIP. pr-go sets no DSN, so in CI
# they would never run. This script gives them a disposable database and turns
# any unexpected skip into a failure. It follows
# tools/scripts/gateway-postgres-tests.sh; the differences are the modules, the
# DSN variables, and ONE allowed skip (below).
#
# Disposable by construction:
#   * The image must already be present locally and is used with --pull never.
#     This script never downloads anything; the workflow pulls one image pinned
#     by digest before calling it.
#   * The container binds to 127.0.0.1 on a Docker-chosen port, uses a random
#     one-off password generated here, and is removed on exit (trap), whatever
#     happens.
#   * The suites create and drop schemas and roles, so they need a superuser
#     and AEGIS_IDENTITY_PG_DISPOSABLE=1. That is acceptable ONLY on an
#     instance like this one, started for the run and destroyed after it.
#     Never point this at a shared or long-lived database.
#   * The DSN reaches `go test` through the environment only. The password is
#     never echoed, is masked in GitHub Actions, and the run fails if it ever
#     appears in the test output.
#   * The DSN is plaintext (sslmode=disable). The services refuse that unless
#     ENVIRONMENT is explicitly development (ADR-0029 "Database TLS"); the test
#     harnesses opt in explicitly. Nothing here weakens the production policy.
#
# The one allowed skip: identity's TestE2EIdentityProcess is not a test on its
# own. It is identity run as a child process by edge-api's end-to-end test, and
# it skips when run directly. Its real coverage is edge-api's
# TestE2E_IdentityTokenAdmittedByEdgeThroughTheRealLifecycle, which must pass.
# Any other skip fails the run.
#
# A second throwaway container serves TLS for pgshared's real verify-full
# tests (ADR-0029 "Database TLS"): a CA, an unrelated CA and a server
# certificate for IP 127.0.0.1 are generated here with openssl for this run
# only, and deleted on exit. Those tests prove that the accepted configuration
# establishes verified TLS and that an untrusted server or a host name the
# certificate does not carry is refused at the handshake.
#
# Usage (from the repository root):
#   POSTGRES_IMAGE=docker.io/library/postgres@sha256:<digest> \
#   GO_TEST_FLAGS=-race \
#     tools/scripts/identity-postgres-tests.sh
# =============================================================================
set -euo pipefail

POSTGRES_IMAGE="${POSTGRES_IMAGE:?set POSTGRES_IMAGE to a locally present image, pinned by digest}"
# Extra `go test` flags, e.g. GO_TEST_FLAGS=-race (needs cgo and a C compiler).
read -r -a GO_TEST_EXTRA <<< "${GO_TEST_FLAGS:-}"

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
modules=(services/go/identity services/go/edge-api)
name="cs-idpg-$(od -An -N4 -tx4 /dev/urandom | tr -d ' \n')"
log="$(mktemp)"
certs="$(mktemp -d)"
started=0

cleanup() {
  if [ "$started" = "1" ]; then
    docker rm -f "$name" "$name-tls" >/dev/null 2>&1 || true
  fi
  rm -f "$log"
  rm -rf "$certs"
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
echo "== identity and edge-api suites against throwaway $POSTGRES_IMAGE on 127.0.0.1:$port"
echo "== server: $(docker exec "$name" psql -U postgres -tAc 'SHOW server_version')"

# ---- The TLS-serving instance. Certificates live for this run only.
# MSYS_NO_PATHCONV keeps Git Bash (local runs on Windows) from rewriting the
# "/CN=" subjects and the volume argument; it has no effect elsewhere.
native() { if command -v cygpath >/dev/null 2>&1; then cygpath -m "$1"; else printf '%s' "$1"; fi; }
(
  cd "$certs"
  export MSYS_NO_PATHCONV=1
  for ca in ca other-ca; do
    openssl ecparam -name prime256v1 -genkey -noout -out "$ca.key" 2>/dev/null
    openssl req -x509 -new -key "$ca.key" -sha256 -days 2 -subj "/CN=aegis-test-$ca" \
      -addext "basicConstraints=critical,CA:TRUE" -addext "keyUsage=critical,keyCertSign,cRLSign" \
      -out "$ca.crt" 2>/dev/null
  done
  openssl ecparam -name prime256v1 -genkey -noout -out server.key 2>/dev/null
  openssl req -new -key server.key -subj "/CN=127.0.0.1" -out server.csr 2>/dev/null
  printf 'subjectAltName=IP:127.0.0.1\nbasicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature\nextendedKeyUsage=serverAuth\n' > server.ext
  openssl x509 -req -in server.csr -CA ca.crt -CAkey ca.key -CAcreateserial -days 2 -sha256 \
    -extfile server.ext -out server.crt 2>/dev/null
  rm -f ca.key other-ca.key server.csr server.ext ca.srl
) || { echo "generating the throwaway test certificates failed" >&2; exit 1; }
certs_native="$(native "$certs")"
# The image requires the key to be owned by postgres with mode 0600, which a
# read-only bind mount cannot give: copy both files in, then start as usual.
MSYS_NO_PATHCONV=1 docker run -d --rm --pull never --name "$name-tls" \
  -e POSTGRES_PASSWORD="$pass" -p 127.0.0.1::5432 -v "$certs_native:/certs:ro" \
  --entrypoint sh "$POSTGRES_IMAGE" -c '
    install -o postgres -g postgres -m 0600 /certs/server.key /var/lib/postgresql/server.key &&
    install -o postgres -g postgres -m 0644 /certs/server.crt /var/lib/postgresql/server.crt &&
    exec docker-entrypoint.sh postgres -c ssl=on \
      -c ssl_cert_file=/var/lib/postgresql/server.crt -c ssl_key_file=/var/lib/postgresql/server.key' >/dev/null
ready=0
for _ in $(seq 1 120); do
  if docker exec "$name-tls" pg_isready -h 127.0.0.1 -U postgres -q >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 0.5
done
if [ "$ready" -ne 1 ]; then
  echo "the throwaway TLS PostgreSQL container did not become ready" >&2
  exit 1
fi
tls_port="$(docker port "$name-tls" 5432/tcp | head -n1 | sed 's/.*://')"
echo "== pgshared TLS suite against throwaway $POSTGRES_IMAGE (ssl=on) on 127.0.0.1:$tls_port"

status=0
for m in "${modules[@]}"; do
  (cd "$repo_root/$m" && \
    AEGIS_IDENTITY_PG_DSN="postgres://postgres:${pass}@127.0.0.1:${port}/postgres?sslmode=disable" \
    AEGIS_IDENTITY_PG_DISPOSABLE=1 \
    go test -count=1 -json "${GO_TEST_EXTRA[@]}" ./...) >> "$log" 2>&1 || status=$?
done
(cd "$repo_root/packages/go/pgshared" && \
  AEGIS_PG_TLS_DSN="postgres://postgres:${pass}@127.0.0.1:${tls_port}/postgres?sslmode=verify-full&sslrootcert=${certs_native}/ca.crt" \
  AEGIS_PG_TLS_OTHER_CA="${certs_native}/other-ca.crt" \
  go test -count=1 -json "${GO_TEST_EXTRA[@]}" ./...) >> "$log" 2>&1 || status=$?

# The password check comes first: if it appears anywhere, no output is echoed.
if grep -qF -- "$pass" "$log"; then
  echo "the throwaway database password appeared in the test output; output withheld" >&2
  exit 1
fi

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

ALLOWED_SKIPS = {
    ("github.com/callscreen/callscreen-platform/services/go/identity/cmd/server", "TestE2EIdentityProcess"),
}
PGSHARED = "github.com/callscreen/callscreen-platform/packages/go/pgshared"
REQUIRED_PASSES = {
    ("github.com/callscreen/callscreen-platform/services/go/edge-api/cmd/server",
     "TestE2E_IdentityTokenAdmittedByEdgeThroughTheRealLifecycle"),
    (PGSHARED, "TestTLS_Postgres_VerifyFullEstablishesVerifiedTLS"),
    (PGSHARED, "TestTLS_Postgres_UntrustedServerIsRefusedAtTheHandshake"),
    (PGSHARED, "TestTLS_Postgres_HostNameMismatchIsRefusedAtTheHandshake"),
}

counts = {"pass": 0, "fail": 0, "skip": 0}
outputs, failed, skipped, passed, packages, raw = {}, [], [], set(), [], []
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
            if action == "fail":
                failed.append(key)
            elif action == "skip":
                skipped.append(key)
            else:
                passed.add(key)
        elif action in ("pass", "fail", "skip") and not key[1]:
            packages.append((action, key[0]))

for action, pkg in packages:
    print(("ok  " if action == "pass" else "FAIL" if action == "fail" else "skip") + "\t" + pkg)
print("== tests (including subtests): pass={pass} fail={fail} skip={skip}".format(**counts))
unexpected = [k for k in skipped if k not in ALLOWED_SKIPS]
for pkg, test in skipped:
    note = "allowed: helper process" if (pkg, test) in ALLOWED_SKIPS else "did not run"
    print(f"== skipped ({note}): {pkg} {test}", file=sys.stderr)
missing = sorted(REQUIRED_PASSES - passed)
for pkg, test in missing:
    print(f"== required test did not pass: {pkg} {test}", file=sys.stderr)
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
sys.exit(2 if unexpected or missing else 0)
PY

if [ "$summary_status" -eq 2 ]; then
  echo "an unexpected test skipped, or a required test did not pass: with a database provided, every test must run" >&2
  status=1
elif [ "$summary_status" -ne 0 ]; then
  echo "could not summarise the test output (exit $summary_status)" >&2
  status=1
fi
exit "$status"
