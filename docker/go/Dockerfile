# =============================================================================
# Shared build for every Go service.
#
# ONE DOCKERFILE, NOT EIGHT. The build differs between services only by which
# module is compiled, so it is parameterised by SERVICE rather than copied. Eight
# near-identical Dockerfiles drift: within a quarter, two will pin different base
# images and one will have quietly lost its non-root user.
#
# Build context is the REPOSITORY ROOT, not this directory, because a service
# module depends on packages/go/* through the workspace.
#
#   docker build -f docker/go/Dockerfile --build-arg SERVICE=edge-api .
# =============================================================================

# --- Stage 1: build ----------------------------------------------------------
# Base images carry an exact patch version, never a floating tag such as
# `golang:1.23`. A floating tag resolves to a different image next week, so
# rebuilding an old commit would not reproduce the artefact that was tested.
#
# DIGEST PINNING IS NOT YET APPLIED. The intended end state is
# `golang:1.23.5-alpine3.21@sha256:<digest>`, which is what actually makes a
# build forensically reconstructable — a patch tag is still mutable in principle.
# The digests are deliberately absent rather than guessed: a wrong digest either
# fails the pull or, worse, pins an image nobody verified.
#
# Renovate populates them on its first run (`pinDigests: true` in
# renovate.json5), which is the correct mechanism because it resolves the digest
# against the registry and keeps it updated with the tag.
FROM golang:1.23.5-alpine3.21 AS build

# git is needed for module resolution; ca-certificates for fetching over TLS.
# --no-cache avoids leaving an apk index in the layer.
#
# Alpine package versions are intentionally unpinned. Alpine's repositories
# retain only the current version of a package, so a pinned version stops
# resolving as soon as upstream publishes a patch — turning every base-image
# refresh into a build break. Reproducibility here comes from the base image
# digest, which fixes the whole filesystem including these packages.
RUN apk add --no-cache git ca-certificates

WORKDIR /src

# Dependency manifests are copied before source so that the module download
# layer is cached and only invalidated when dependencies actually change.
# Copying source first would re-download every dependency on every source edit,
# which on a constrained network is the difference between a 20-second and a
# five-minute build.
COPY go.work go.work.sum* ./
COPY packages/go/platform/go.mod packages/go/platform/go.sum* ./packages/go/platform/
COPY packages/go/eventbus/go.mod packages/go/eventbus/go.sum* ./packages/go/eventbus/
COPY packages/go/contracts-go/go.mod packages/go/contracts-go/go.sum* ./packages/go/contracts-go/

ARG SERVICE
COPY services/go/${SERVICE}/go.mod services/go/${SERVICE}/go.sum* ./services/go/${SERVICE}/

RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download all

# Source is copied after dependencies so a code change reuses the module cache.
COPY packages/go ./packages/go
COPY services/go/${SERVICE} ./services/go/${SERVICE}

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME

WORKDIR /src/services/go/${SERVICE}

# CGO_ENABLED=0 produces a statically linked binary so the runtime stage needs
# no libc at all, which is what makes a scratch-based final image possible.
#
# -trimpath removes local filesystem paths from the binary. Without it, the
# build machine's directory layout is embedded in every stack trace and shipped
# to production — an information disclosure and a reproducibility break, since
# two machines would produce different bytes from identical source.
#
# -s -w strip the symbol table and DWARF data, roughly halving binary size.
# Stack traces still carry function names and line numbers, so debuggability is
# unaffected.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux \
    go build \
      -trimpath \
      -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildTime=${BUILD_TIME}" \
      -o /out/service \
      ./cmd/server

# --- Stage 2: runtime --------------------------------------------------------
# scratch, not alpine. The binary is static, so the image needs nothing but the
# binary, TLS roots and a passwd entry. This yields an image of a few megabytes
# with, crucially, NO SHELL AND NO PACKAGE MANAGER: an attacker achieving
# execution inside the container has no tools to pivot with, and the image has
# essentially no CVE surface to scan.
#
# The trade is that `docker exec` for debugging is impossible. That is
# deliberate — production debugging goes through logs, metrics and traces, and a
# shell in a production container is a control most compliance regimes flag.
# Engineers who need a shell build the debug variant locally.
FROM scratch AS runtime

# TLS roots, required for any outbound HTTPS call.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# A passwd entry so the process runs as a real non-root user rather than as a
# bare numeric uid. Some Kubernetes admission policies reject the latter.
COPY --from=build /etc/passwd /etc/passwd
COPY --from=build /etc/group /etc/group

COPY --from=build /out/service /service

# nobody:nogroup. Running as root inside a container is the default and is
# wrong: a container escape then starts from root on the host.
USER 65534:65534

# Documentation only; publishing is decided by the orchestrator. 8080 is the
# application listener, 8081 the probe listener — kept separate so probes are
# answered while the main listener drains (see packages/go/platform/health.go).
EXPOSE 8080 8081

# No HEALTHCHECK instruction. Kubernetes performs its own liveness and readiness
# probes against /healthz and /readyz; a Docker-level healthcheck would be a
# second, uncoordinated source of truth, and scratch has no shell to run it in.

ENTRYPOINT ["/service"]
