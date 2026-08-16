# =============================================================================
# Shared build for every Python service.
#
# Parameterised by SERVICE for the same reason as the Go image: six
# near-identical Dockerfiles drift, and the drift is only discovered when one
# service behaves differently under load.
#
# Build context is the REPOSITORY ROOT, because a service depends on
# packages/python/* through the uv workspace.
#
#   docker build -f docker/python/Dockerfile --build-arg SERVICE=fraud-engine .
# =============================================================================

# --- Stage 1: build ----------------------------------------------------------
# Exact patch version, never a floating tag, for the reasons set out in
# docker/go/Dockerfile. Digests are added by Renovate on its first run
# (`pinDigests: true`); they are left absent here rather than guessed, because a
# wrong digest either fails the pull or pins an unverified image.
FROM python:3.12.8-slim-bookworm AS build

# uv is copied from its own published image rather than installed with pip.
# Installing it with pip would place it in the same environment as the
# application's dependencies, where it could influence resolution of its own
# inputs. Copying the static binary keeps the tool and the dependency tree
# strictly separate.
COPY --from=ghcr.io/astral-sh/uv:0.5.14 /uv /uvx /bin/

ENV UV_COMPILE_BYTECODE=1 \
    UV_LINK_MODE=copy \
    UV_PYTHON_DOWNLOADS=never \
    PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1

WORKDIR /src

ARG SERVICE

# Manifests before source, so the dependency layer is cached and invalidated
# only by an actual dependency change.
COPY pyproject.toml uv.lock ./
COPY packages/python/platform/pyproject.toml ./packages/python/platform/
COPY packages/python/ai-primitives/pyproject.toml ./packages/python/ai-primitives/
COPY packages/python/contracts-py/pyproject.toml ./packages/python/contracts-py/
COPY services/python/${SERVICE}/pyproject.toml ./services/python/${SERVICE}/

# --frozen fails if uv.lock does not match the manifests, rather than silently
# re-resolving. A container build that quietly resolves different versions from
# the ones the test suite ran against is the exact class of drift lockfiles
# exist to prevent.
#
# --no-dev omits the linting and test toolchain. Shipping ruff, mypy and pytest
# into production adds tens of megabytes and enlarges the vulnerability surface
# for tools that will never run there.
RUN --mount=type=cache,target=/root/.cache/uv \
    uv sync --frozen --no-dev --no-install-project --package "${SERVICE}"

COPY packages/python ./packages/python
COPY services/python/${SERVICE} ./services/python/${SERVICE}

RUN --mount=type=cache,target=/root/.cache/uv \
    uv sync --frozen --no-dev --package "${SERVICE}"

# --- Stage 2: runtime --------------------------------------------------------
# Python cannot use scratch the way the Go image does: it needs the interpreter
# and its shared libraries. slim-bookworm is the smallest base that keeps the
# standard library complete.
#
# Alpine is deliberately NOT used. musl libc forces source builds of any wheel
# without a musl variant — which includes much of the scientific stack the AI
# tier will eventually need — producing slower builds, larger images and
# occasional numerical differences from the glibc wheels used in development.
FROM python:3.12.8-slim-bookworm AS runtime

ARG SERVICE
ARG VERSION=dev
ARG COMMIT=unknown

# A dedicated unprivileged user. Running as root inside a container means a
# container escape begins with root on the host.
RUN groupadd --system --gid 65532 nonroot \
 && useradd --system --uid 65532 --gid nonroot --no-create-home --shell /usr/sbin/nologin nonroot

WORKDIR /app

# Only the resolved virtual environment and the application source are carried
# forward. uv itself, the build caches and the compilers stay in the build stage.
COPY --from=build --chown=nonroot:nonroot /src/.venv /app/.venv
COPY --from=build --chown=nonroot:nonroot /src/packages/python /app/packages/python
COPY --from=build --chown=nonroot:nonroot /src/services/python/${SERVICE} /app/services/python/${SERVICE}

# Putting the venv first on PATH means `python` resolves to the project
# interpreter without any activation script, which would need a shell.
ENV PATH="/app/.venv/bin:$PATH" \
    PYTHONPATH="/app" \
    PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    CS_VERSION="${VERSION}" \
    CS_COMMIT="${COMMIT}"

# SERVICE is an ARG, which does not survive into the running container. It is
# promoted to an ENV so the entrypoint can resolve which module to execute.
ENV CS_SERVICE_MODULE="${SERVICE}"

USER 65532:65532

EXPOSE 8080 8081

# Exec form, not shell form. Shell form wraps the process in /bin/sh, which
# becomes PID 1 and does not forward SIGTERM — so graceful shutdown never runs
# and every deployment kills in-flight work after the grace period expires.
# This one line is the difference between zero-downtime deploys and dropped
# requests on every release.
ENTRYPOINT ["sh", "-c", "exec python -m ${CS_SERVICE_MODULE//-/_}"]
