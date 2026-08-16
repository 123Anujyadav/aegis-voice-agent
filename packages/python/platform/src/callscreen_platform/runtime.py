"""Service lifecycle: startup, probe serving, signal handling and graceful stop.

The Python counterpart of the ``Service`` type in ``packages/go/platform``,
implementing the same shutdown sequence. Correct graceful shutdown is subtle and
every service needs identical behaviour; implemented ad hoc in six entrypoints
it will be implemented six slightly different ways, and the differences will
only be discovered during an incident.

The probe listener is built on ``asyncio`` primitives from the standard library
rather than on a web framework. That keeps the mandatory dependency set of every
service small, and means a worker-only service (a Kafka consumer with no HTTP
surface of its own) still answers probes without carrying a framework it never
otherwise uses.
"""

from __future__ import annotations

import asyncio
from collections.abc import Awaitable, Callable
import contextlib
import json
import signal
import sys
from typing import Any, Protocol

from callscreen_platform.config import ServiceConfig
from callscreen_platform.health import Health
from callscreen_platform.logging import configure_logging, get_logger

# Upper bound on a probe request line plus headers. Bounding the read prevents a
# client from streaming an unbounded request into memory, which is a trivial
# denial of service against an endpoint that is deliberately unauthenticated.
_MAX_REQUEST_BYTES = 8192


class Runner(Protocol):
    """A long-lived component managed by the service lifecycle.

    Every background worker implements this so the lifecycle can start it,
    observe its failure, and stop it deterministically. Components that manage
    their own tasks outside this contract are invisible to shutdown, and are the
    usual reason a process has to be killed rather than drained.
    """

    @property
    def name(self) -> str:
        """Identifier used in lifecycle logs."""
        ...

    async def run(self) -> None:
        """Execute the component until cancelled or unrecoverably failed."""
        ...

    async def shutdown(self) -> None:
        """Release resources. Called after ``run`` has been cancelled."""
        ...


class Service:
    """Composes configuration, logging, health and runners into one lifecycle."""

    def __init__(self, config: ServiceConfig) -> None:
        """Initialise the lifecycle.

        Logging is configured here, before anything else, so that any failure
        during subsequent initialisation is itself logged in structured form.

        Args:
            config: Validated service configuration.
        """
        configure_logging(config)
        self._config = config
        self._log = get_logger(config.service_name)
        self._health = Health(config)
        self._runners: list[Runner] = []
        self._stop = asyncio.Event()

    @property
    def config(self) -> ServiceConfig:
        """The service's validated configuration."""
        return self._config

    @property
    def health(self) -> Health:
        """The service's health aggregator."""
        return self._health

    def register(self, runner: Runner) -> None:
        """Add a runner to the lifecycle.

        Registration order is dependency order, lowest-level first: shutdown
        runs in reverse, so a component tears down before whatever it depends on.

        Args:
            runner: The component to manage.
        """
        self._runners.append(runner)

    async def run(self) -> int:
        """Start every runner plus the probe listener and block until stopped.

        The shutdown sequence, and why each step is ordered as it is:

        1. SIGTERM arrives. Kubernetes has already begun removing this pod from
           Endpoints, but that removal is eventually consistent — proxies on
           other nodes may keep routing to us for several seconds.
        2. Readiness begins failing immediately. This is what actually tells the
           load balancer to stop. It is first because everything after it is
           wasted if traffic is still arriving.
        3. We wait out the drain delay, the window in which the balancer
           observes the change. Without it we close the listener while requests
           are still routed to us, and those requests fail — precisely the
           dropped-request symptom graceful shutdown is meant to eliminate.
        4. Runners are cancelled and torn down in reverse registration order.

        Returns:
            A process exit code: 0 on orderly shutdown, 1 if a runner failed.
        """
        self._install_signal_handlers()

        probe_server = await asyncio.start_server(
            self._handle_probe,
            host="0.0.0.0",  # noqa: S104 -- must bind all interfaces inside a container
            port=self._config.health_port,
        )
        self._log.info("probe listener bound", port=self._config.health_port)

        # Typed as Task[Any] rather than Task[None] because the wait set below
        # is heterogeneous: runner tasks complete with None, while the stop
        # task completes with the bool returned by Event.wait. Narrowing the
        # dict to Task[None] would make that set ill-typed.
        tasks: dict[asyncio.Task[Any], Runner] = {}
        for runner in self._runners:
            task: asyncio.Task[Any] = asyncio.create_task(runner.run(), name=runner.name)
            tasks[task] = runner
            self._log.info("runner starting", runner=runner.name)

        self._health.mark_ready()
        self._log.info("service ready", runners=len(self._runners))

        exit_code = 0
        stop_task: asyncio.Task[Any] = asyncio.create_task(self._stop.wait(), name="stop-signal")

        try:
            done, _ = await asyncio.wait(
                {stop_task, *tasks.keys()},
                return_when=asyncio.FIRST_COMPLETED,
            )
            for task in done:
                if task is stop_task:
                    self._log.info("shutdown signal received")
                    continue
                runner = tasks[task]
                if (exc := task.exception()) is not None:
                    exit_code = 1
                    self._log.error(
                        "runner failed, shutting down service",
                        runner=runner.name,
                        error=str(exc),
                    )
                else:
                    self._log.warning(
                        "runner exited unexpectedly without error",
                        runner=runner.name,
                    )
        finally:
            stop_task.cancel()
            with contextlib.suppress(asyncio.CancelledError):
                await stop_task
            await self._shutdown(tasks, probe_server)

        return exit_code

    async def _shutdown(
        self,
        tasks: dict[asyncio.Task[Any], Runner],
        probe_server: asyncio.Server,
    ) -> None:
        """Perform the ordered teardown described on :meth:`run`.

        Args:
            tasks: Running runner tasks keyed by their owning runner.
            probe_server: The probe listener, stopped last so probes stay
                honest for as long as possible.
        """
        self._health.mark_shutting_down()
        self._log.info("readiness disabled, draining")

        # Derived from the shutdown budget rather than hard-coded, so a service
        # with a short grace period does not spend all of it waiting.
        drain_delay = min(self._config.shutdown_timeout_seconds / 5, 5.0)
        await asyncio.sleep(drain_delay)

        for task, runner in reversed(list(tasks.items())):
            if not task.done():
                task.cancel()
            with contextlib.suppress(asyncio.CancelledError, Exception):
                await task

            self._log.info("stopping runner", runner=runner.name)
            try:
                async with asyncio.timeout(self._config.shutdown_timeout_seconds):
                    await runner.shutdown()
            except TimeoutError:
                self._log.error("runner shutdown timed out", runner=runner.name)
            except Exception as exc:
                self._log.error("runner shutdown failed", runner=runner.name, error=str(exc))

        probe_server.close()
        await probe_server.wait_closed()
        self._log.info("shutdown complete")

    def _install_signal_handlers(self) -> None:
        """Route SIGTERM and SIGINT to the stop event.

        ``loop.add_signal_handler`` is unavailable on Windows, where developers
        run the service locally; the fallback keeps the local inner loop working
        even though production is Linux-only.
        """
        loop = asyncio.get_running_loop()
        for sig in (signal.SIGTERM, signal.SIGINT):
            try:
                loop.add_signal_handler(sig, self._stop.set)
            except (NotImplementedError, AttributeError):
                signal.signal(sig, lambda *_: self._stop.set())

    async def _handle_probe(
        self, reader: asyncio.StreamReader, writer: asyncio.StreamWriter
    ) -> None:
        """Serve one liveness or readiness probe request.

        Args:
            reader: Inbound stream.
            writer: Outbound stream.
        """
        try:
            request = await asyncio.wait_for(reader.read(_MAX_REQUEST_BYTES), timeout=5.0)
            line = request.split(b"\r\n", 1)[0].decode("latin-1", errors="replace")
            parts = line.split(" ")
            path = parts[1] if len(parts) > 1 else "/"

            if path.startswith("/healthz"):
                payload, status = self._health.liveness(), 200
            elif path.startswith("/readyz"):
                payload, status = await self._health.readiness()
            else:
                payload, status = {"error": "not found"}, 404

            body = json.dumps(payload).encode("utf-8")
            reason = {200: "OK", 404: "Not Found", 503: "Service Unavailable"}[status]
            writer.write(
                b"HTTP/1.1 %d %s\r\n"
                b"Content-Type: application/json; charset=utf-8\r\n"
                b"Content-Length: %d\r\n"
                # Probe responses must never be cached: an intermediary holding a
                # healthy response would mask a subsequent failure, the worst
                # possible failure mode for a health endpoint.
                b"Cache-Control: no-store\r\n"
                b"Connection: close\r\n"
                b"\r\n" % (status, reason.encode(), len(body)) + body
            )
            await writer.drain()
        except (TimeoutError, ConnectionError, OSError):
            # A probe client that disconnects mid-request is routine, not an
            # error worth logging at volume.
            pass
        finally:
            writer.close()
            with contextlib.suppress(ConnectionError, OSError):
                await writer.wait_closed()


def run_service(
    config_factory: Callable[[], ServiceConfig],
    setup: Callable[[Service], Awaitable[None]] | None = None,
) -> int:
    """Entrypoint helper shared by every service's ``__main__``.

    Configuration failures are reported to stderr and exit non-zero **before**
    logging is configured, because a service that cannot read its configuration
    cannot be trusted to have configured its logger either.

    Args:
        config_factory: Builds and validates the service's configuration.
        setup: Optional coroutine registering runners and health checks before
            the lifecycle starts.

    Returns:
        A process exit code suitable for ``sys.exit``.
    """
    try:
        config = config_factory()
    except Exception as exc:
        print(f"FATAL: {exc}", file=sys.stderr)  # noqa: T201 -- pre-logging path
        return 78  # EX_CONFIG from sysexits.h

    async def main() -> int:
        service = Service(config)
        if setup is not None:
            await setup(service)
        return await service.run()

    return asyncio.run(main())
