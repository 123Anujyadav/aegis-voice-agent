#!/usr/bin/env bash
# =============================================================================
# Smoke checks of a Go service image built from docker/go/Dockerfile, with no
# configuration, no network to anything, and no registry:
#
#   - it runs as nobody (65534:65534), never root;
#   - the binary starts in the scratch image (static, nothing missing) and logs
#     the commit it was built from;
#   - it does what the service is documented to do with no configuration:
#       identity  boots inactive (internal/bootstrap activation contract):
#                 /healthz 200, /readyz names "identity inactive";
#       edge-api  refuses to start: protected routes without authentication.
#
# This shows the image is buildable and starts. It is not a deployment, and
# says nothing about a configured service.
#
# Usage: image-smoke.sh <service> <image> <commit>
# =============================================================================
set -euo pipefail

service="${1:?service}"
image="${2:?image}"
commit="${3:?commit}"

user="$(docker image inspect --format '{{.Config.User}}' "${image}")"
test "${user}" = "65534:65534" || { echo "::error::${image} runs as '${user}', not 65534:65534"; exit 1; }
echo "user: ${user}; size: $(docker image inspect --format '{{.Size}}' "${image}") bytes"

case "${service}" in
identity)
    port=18081
    cid="$(docker run -d --rm -p "127.0.0.1:${port}:8081" "${image}")"
    trap 'docker logs "${cid}" >"${RUNNER_TEMP:-/tmp}/${service}.log" 2>&1 || true; docker stop "${cid}" >/dev/null 2>&1 || true' EXIT
    up=""
    for _ in $(seq 1 30); do
        if [ "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:${port}/healthz" || true)" = "200" ]; then
            up=yes
            break
        fi
        sleep 1
    done
    test -n "${up}" || { echo "::error::identity's /healthz did not answer 200 within 30 s"; docker logs "${cid}" | tail -20; exit 1; }
    ready="$(curl -s "http://127.0.0.1:${port}/readyz")"
    echo "readyz: ${ready}"
    grep -q 'identity inactive' <<<"${ready}" || { echo "::error::identity's /readyz does not report the inactive state"; exit 1; }
    logs="$(docker logs "${cid}" 2>&1)"
    ;;
edge-api)
    status=0
    logs="$(timeout 30 docker run --rm --network none "${image}" 2>&1)" || status=$?
    test "${status}" -ne 124 || { echo "::error::edge-api did not exit within 30 s"; exit 1; }
    test "${status}" -ne 0 || { echo "::error::edge-api started with no authentication configured"; exit 1; }
    grep -q 'authentication is not configured' <<<"${logs}" || { echo "::error::edge-api exited ${status} for another reason"; echo "${logs}" | tail -20; exit 1; }
    ;;
*)
    echo "::error::no smoke check for ${service}"
    exit 1
    ;;
esac

if grep -q 'panic:' <<<"${logs}"; then
    echo "::error::${service} panicked"
    echo "${logs}" | tail -40
    exit 1
fi
grep -q "\"commit\":\"${commit}\"" <<<"${logs}" || { echo "::error::${service} does not log the commit it was built from (${commit})"; exit 1; }
echo "${service}: ok"
