"""Liveness and readiness reporting.

The Python counterpart of ``packages/go/platform/health.go``, with identical
semantics so that a Kubernetes probe configuration is portable between tiers.

The distinction this module exists to enforce
---------------------------------------------
**Liveness** answers "is this process broken beyond recovery?" A failing
liveness probe causes a **restart**. It must therefore never consult a
dependency: if the database is down and liveness checks it, every replica
restarts in a loop, converting a database incident into a total outage plus a
thundering herd on recovery.

**Readiness** answers "should this instance receive traffic right now?" A
failing readiness probe removes the instance from the load balancer but leaves
it running. This is where dependency checks belong.

Conflating the two is among the most common and most damaging Kubernetes
misconfigurations, so the two are separate coroutines here and cannot be wired
to the same handler by accident.
"""

from __future__ import annotations

import asyncio
from collections.abc import Awaitable, Callable
from dataclasses import dataclass, field
from enum import StrEnum
import time
from typing import Any

from callscreen_platform.config import ServiceConfig


class HealthStatus(StrEnum):
    """State reported by a dependency or by the service overall."""

    HEALTHY = "healthy"
    """Normal operation."""

    DEGRADED = "degraded"
    """Serving, but with reduced capability.

    Deliberately distinct from ``UNHEALTHY``. A service that removes itself from
    load balancing because its cache is unreachable converts a performance
    problem into an outage. ``DEGRADED`` means "keep sending me traffic, but
    page someone".
    """

    UNHEALTHY = "unhealthy"
    """Cannot serve correctly; must not receive traffic."""


@dataclass(frozen=True, slots=True)
class CheckResult:
    """Outcome of evaluating a single dependency."""

    name: str
    """Dependency identifier, for example ``postgres`` or ``kafka``."""

    status: HealthStatus
    """Evaluated health of the dependency."""

    duration_ms: int
    """How long the check took.

    Frequently the earliest signal of impending failure: latency degrades before
    availability does.
    """

    critical: bool
    """Whether failure makes the whole service unhealthy rather than degraded."""

    message: str = ""
    """Engineer-facing detail on a non-healthy result.

    Empty when healthy, keeping probe responses small — probes run every few
    seconds forever.
    """

    def to_dict(self) -> dict[str, Any]:
        """Render the result for a JSON probe response.

        Returns:
            A mapping with ``message`` omitted when empty.
        """
        payload: dict[str, Any] = {
            "name": self.name,
            "status": str(self.status),
            "duration_ms": self.duration_ms,
            "critical": self.critical,
        }
        if self.message:
            payload["message"] = self.message
        return payload


# A check returns None on success and raises on failure. Raising rather than
# returning a bool means the failure carries its own explanation, which lands in
# the probe response and saves an engineer a round trip to the logs.
CheckFn = Callable[[], Awaitable[None]]


@dataclass(slots=True)
class _RegisteredCheck:
    """A dependency check plus the metadata governing its interpretation."""

    name: str
    fn: CheckFn
    critical: bool
    timeout_seconds: float


@dataclass(slots=True)
class Health:
    """Aggregates dependency checks and answers liveness and readiness probes.

    A service begins **not ready**. Readiness is asserted explicitly via
    :meth:`mark_ready` once initialisation completes, so an instance cannot
    receive traffic before its connection pools are warm.
    """

    config: ServiceConfig
    _checks: list[_RegisteredCheck] = field(default_factory=list, init=False)
    _ready: bool = field(default=False, init=False)
    _shutting_down: bool = field(default=False, init=False)
    _started_at: float = field(default_factory=time.monotonic, init=False)

    def register(
        self,
        name: str,
        check: CheckFn,
        *,
        critical: bool,
        timeout_seconds: float = 2.0,
    ) -> None:
        """Register a dependency check evaluated during readiness.

        Args:
            name: Stable dependency identifier.
            check: Coroutine raising on failure and returning ``None`` on
                success. It must respect cancellation; a check that ignores its
                timeout turns a readiness probe into a probe timeout, which the
                orchestrator reads as failure and answers with a restart.
            critical: When ``True``, failure makes the service unhealthy and
                removes it from load balancing. When ``False``, failure reports
                degraded and traffic keeps flowing.
            timeout_seconds: Upper bound on this individual check. Must be
                comfortably shorter than the orchestrator's probe timeout, or a
                slow check produces a probe timeout whose cause is invisible in
                the response body.
        """
        self._checks.append(
            _RegisteredCheck(
                name=name,
                fn=check,
                critical=critical,
                timeout_seconds=timeout_seconds,
            )
        )

    def mark_ready(self) -> None:
        """Declare the service ready to receive traffic."""
        self._ready = True

    def mark_shutting_down(self) -> None:
        """Record that graceful shutdown has begun.

        This is what makes zero-downtime deployment work. On SIGTERM the service
        marks itself shutting down and immediately begins failing readiness, so
        the load balancer stops sending new requests. Only after the balancer
        has observed that — which takes one or more probe intervals — does the
        process stop accepting connections and drain. Skipping this step is why
        deployments that look graceful still drop requests.
        """
        self._shutting_down = True
        self._ready = False

    @property
    def uptime_seconds(self) -> int:
        """Seconds since the aggregator was constructed."""
        return int(time.monotonic() - self._started_at)

    def liveness(self) -> dict[str, Any]:
        """Answer the liveness probe.

        Answers purely from process-local state and never consults a dependency,
        for the reason documented in the module docstring.

        Returns:
            A payload reporting healthy whenever the process can serve at all.
        """
        return {
            "status": str(HealthStatus.HEALTHY),
            "service": self.config.service_name,
            "version": self.config.version,
            "uptime_seconds": self.uptime_seconds,
        }

    async def readiness(self) -> tuple[dict[str, Any], int]:
        """Answer the readiness probe, evaluating every dependency concurrently.

        Checks run in parallel rather than in sequence because probe latency is
        bounded by the orchestrator: ten sequential 200 ms checks exceed a
        typical one-second probe timeout, while ten concurrent ones do not.

        Returns:
            A tuple of the response payload and the HTTP status code — 200 when
            healthy or degraded, 503 when unhealthy.
        """
        if self._shutting_down:
            # The answer cannot change during shutdown, and running checks would
            # waste time from the drain budget.
            return (
                {
                    "status": str(HealthStatus.UNHEALTHY),
                    "service": self.config.service_name,
                    "version": self.config.version,
                    "uptime_seconds": self.uptime_seconds,
                    "checks": [],
                },
                503,
            )

        results = await self._run_checks()

        status = HealthStatus.HEALTHY
        for result in results:
            if result.status is HealthStatus.UNHEALTHY and result.critical:
                status = HealthStatus.UNHEALTHY
                break
            if result.status is not HealthStatus.HEALTHY:
                status = HealthStatus.DEGRADED

        if not self._ready:
            status = HealthStatus.UNHEALTHY

        payload = {
            "status": str(status),
            "service": self.config.service_name,
            "version": self.config.version,
            "uptime_seconds": self.uptime_seconds,
            "checks": [r.to_dict() for r in results],
        }
        return payload, 503 if status is HealthStatus.UNHEALTHY else 200

    async def _run_checks(self) -> list[CheckResult]:
        """Evaluate every registered dependency concurrently.

        Returns:
            One result per registered check, in registration order.
        """
        if not self._checks:
            return []

        async def run_one(registered: _RegisteredCheck) -> CheckResult:
            started = time.monotonic()
            try:
                async with asyncio.timeout(registered.timeout_seconds):
                    await registered.fn()
            except TimeoutError:
                return CheckResult(
                    name=registered.name,
                    status=HealthStatus.UNHEALTHY,
                    duration_ms=int((time.monotonic() - started) * 1000),
                    critical=registered.critical,
                    message=f"timed out after {registered.timeout_seconds}s",
                )
            except Exception as exc:
                # A health check that raises must not take down the probe
                # handler; an unanswerable probe is read as a hard failure.
                return CheckResult(
                    name=registered.name,
                    status=HealthStatus.UNHEALTHY,
                    duration_ms=int((time.monotonic() - started) * 1000),
                    critical=registered.critical,
                    message=f"{type(exc).__name__}: {exc}",
                )
            return CheckResult(
                name=registered.name,
                status=HealthStatus.HEALTHY,
                duration_ms=int((time.monotonic() - started) * 1000),
                critical=registered.critical,
            )

        return list(await asyncio.gather(*(run_one(c) for c in self._checks)))
