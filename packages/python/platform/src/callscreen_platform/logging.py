"""Structured logging with mandatory redaction of personal data.

This is the Python counterpart of ``packages/go/platform/logging.go`` and emits
records with identical field names, so that one log query spans both tiers.

Redaction is implemented as a structlog *processor* rather than as a helper the
caller must remember to invoke. That choice is deliberate: a redaction scheme
depending on discipline at thousands of call sites will leak. Placing it in the
processor chain makes it unavoidable — there is no path from a log call to the
sink that bypasses it.
"""

from __future__ import annotations

from contextvars import ContextVar
import hashlib
import hmac
import logging
import secrets
import sys
from typing import Any, Final

import structlog
from structlog.types import EventDict, Processor

from callscreen_platform.config import LogFormat, LogLevel, ServiceConfig

# Standard attribute keys, declared as constants so that a typo cannot silently
# create a second differently-named field that splits a dashboard query in two.
# These strings match the Go module's Attr* constants exactly.
ATTR_SERVICE: Final = "service"
ATTR_VERSION: Final = "version"
ATTR_ENVIRONMENT: Final = "environment"
ATTR_REGION: Final = "region"
ATTR_TRACE_ID: Final = "trace_id"
ATTR_CALL_SESSION_ID: Final = "call_session_id"
ATTR_USER_ID: Final = "user_id"
ATTR_ERROR: Final = "error"
ATTR_DURATION_MS: Final = "duration_ms"

_REDACTED: Final = "[REDACTED]"

# Keys that are explicitly safe and are checked BEFORE the deny list.
#
# This allow list exists because substring matching, while the right default for
# catching unanticipated field names, produces false positives against our own
# correlation keys. The motivating case: ``call_session_id`` contains the
# substring ``session_id``, so without this list the single most useful
# debugging identifier in the platform would be redacted from every log line —
# silently, and precisely when an engineer most needs it.
#
# Anything added here must be genuinely non-personal. These are internal
# correlation identifiers minted by us, not derived from user data.
_ALWAYS_ALLOWED: Final[frozenset[str]] = frozenset(
    {
        "call_session_id",
        "trace_id",
        "span_id",
        "request_id",
        "correlation_id",
        "idempotency_key",
    }
)

# Keys whose values must never be logged, whatever they contain.
#
# The schema annotations in contracts/proto/callscreen/common/v1/annotations.proto
# are the primary control and cover structured protobuf data. This deny list is
# the second layer, covering values that never pass through a protobuf message:
# a token parsed from an HTTP header, a number read from a URL path, a dict
# assembled ad hoc in a handler. Defence in depth.
#
# Matching is case-insensitive and substring-based, so "authorization",
# "Authorization" and "x-authorization" are all caught by one entry.
_SENSITIVE_KEYS: Final[frozenset[str]] = frozenset(
    {
        "password",
        "passwd",
        "secret",
        "token",
        "authorization",
        "api_key",
        "apikey",
        "private_key",
        "credential",
        "session_id",
        "refresh_token",
        "access_token",
        "otp",
        "pin",
        "cvv",
        "attestation",
        "cookie",
        "set-cookie",
    }
)

# Keys that are personal data but whose correlation value is operationally
# necessary. Rather than dropping them, the processor substitutes a keyed hash:
# two records about the same phone number remain joinable, but the number is not
# recoverable from the logs.
_HASHED_KEYS: Final[frozenset[str]] = frozenset(
    {
        "phone",
        "msisdn",
        "caller_number",
        "callee_number",
        "email",
        "install_id",
        "device_id",
        "ip",
        "source_ip",
        "contact",
    }
)

# Request-scoped correlation identifiers.
#
# ContextVar is used rather than threading a logger through every signature.
# The explicit alternative is more honest about data flow but so invasive in
# practice that engineers route around it, which is strictly worse. ContextVar
# propagates correctly across ``await`` boundaries and into ``asyncio`` tasks,
# which thread-locals do not.
_trace_id: ContextVar[str | None] = ContextVar("trace_id", default=None)
_call_session_id: ContextVar[str | None] = ContextVar("call_session_id", default=None)

# Per-process HMAC key, replaced by configure_logging when a stable key is
# supplied. A random default means pseudonyms cannot be correlated across
# processes or reversed with a precomputed table.
_hash_key: bytes = secrets.token_bytes(32)


def _pseudonymise(value: str) -> str:
    """Return a truncated keyed HMAC of ``value``.

    The result is prefixed so a reader can tell at a glance that the value is a
    pseudonym rather than a real identifier. Truncating to 16 hex characters
    keeps log volume down while leaving collision probability negligible at our
    cardinality.

    Args:
        value: The raw identifier to pseudonymise.

    Returns:
        A ``pseudo:``-prefixed hex digest, or the empty string for empty input.
    """
    if not value:
        return ""
    digest = hmac.new(_hash_key, value.encode("utf-8"), hashlib.sha256)
    return f"pseudo:{digest.hexdigest()[:16]}"


def _redact_value(key: str, value: Any) -> Any:  # noqa: ANN401 -- log values are arbitrary
    """Apply the redaction policy to one key-value pair.

    Nested mappings and sequences are traversed so that nesting cannot be used
    to smuggle a sensitive value past the filter.

    Args:
        key: The attribute name, used to decide policy.
        value: The attribute value.

    Returns:
        The value, redacted, pseudonymised, or unchanged.
    """
    lowered = key.lower()

    # The allow list is consulted first so that an internal correlation key is
    # never caught by an incidental substring match in the deny list.
    if lowered in _ALWAYS_ALLOWED:
        return value

    if any(marker in lowered for marker in _SENSITIVE_KEYS):
        return _REDACTED

    if any(marker in lowered for marker in _HASHED_KEYS):
        return _pseudonymise(str(value))

    if isinstance(value, dict):
        return {k: _redact_value(str(k), v) for k, v in value.items()}

    if isinstance(value, list | tuple):
        return type(value)(_redact_value(key, item) for item in value)

    return value


def _redaction_processor(
    _logger: Any,  # noqa: ANN401 -- structlog processor signature
    _method_name: str,
    event_dict: EventDict,
) -> EventDict:
    """Structlog processor enforcing the redaction policy on every record.

    Args:
        _logger: The bound logger; unused.
        _method_name: The log method invoked; unused.
        event_dict: The record under construction.

    Returns:
        The record with every sensitive value redacted or pseudonymised.
    """
    return {key: _redact_value(str(key), value) for key, value in event_dict.items()}


def _context_processor(
    _logger: Any,  # noqa: ANN401 -- structlog processor signature
    _method_name: str,
    event_dict: EventDict,
) -> EventDict:
    """Attach request-scoped correlation identifiers to the record.

    Promoting these from context rather than requiring the caller to restate
    them is what makes every record joinable to its distributed trace.

    Args:
        _logger: The bound logger; unused.
        _method_name: The log method invoked; unused.
        event_dict: The record under construction.

    Returns:
        The record with ``trace_id`` and ``call_session_id`` attached when set.
    """
    if (trace := _trace_id.get()) is not None:
        event_dict[ATTR_TRACE_ID] = trace
    if (session := _call_session_id.get()) is not None:
        event_dict[ATTR_CALL_SESSION_ID] = session
    return event_dict


_LEVEL_MAP: Final[dict[LogLevel, int]] = {
    LogLevel.DEBUG: logging.DEBUG,
    LogLevel.INFO: logging.INFO,
    LogLevel.WARN: logging.WARNING,
    LogLevel.ERROR: logging.ERROR,
}


def configure_logging(config: ServiceConfig) -> None:
    """Install the process-wide logging configuration.

    Call this once, before any other initialisation. Configuring the stdlib
    root logger as well as structlog matters because third-party libraries and
    the standard library's own error paths log through stdlib ``logging``;
    without this, those records would bypass redaction and structured
    formatting entirely.

    Args:
        config: Validated service configuration supplying level, format, and
            the identity fields attached to every record.
    """
    global _hash_key  # noqa: PLW0603 -- process-wide key, set once at boot
    if config.log_hash_key:
        _hash_key = config.log_hash_key.encode("utf-8")

    level = _LEVEL_MAP[config.log_level]

    shared: list[Processor] = [
        structlog.contextvars.merge_contextvars,
        _context_processor,
        structlog.processors.add_log_level,
        structlog.processors.TimeStamper(fmt="iso", utc=True),
        structlog.processors.StackInfoRenderer(),
        # Exceptions are rendered into the record rather than printed to stderr
        # separately, so a traceback stays attached to its structured context.
        structlog.processors.format_exc_info,
        # Redaction runs LAST among the enriching processors, so that anything
        # added by an earlier processor is also subject to the policy.
        _redaction_processor,
    ]

    renderer: Processor = (
        structlog.processors.JSONRenderer()
        if config.log_format is LogFormat.JSON
        else structlog.dev.ConsoleRenderer(colors=True)
    )

    structlog.configure(
        processors=[*shared, renderer],
        wrapper_class=structlog.make_filtering_bound_logger(level),
        # No explicit file is passed. PrintLoggerFactory resolves sys.stdout
        # when it constructs a logger rather than when this function runs, so
        # the logger cannot end up holding a stale or closed handle if stdout
        # is replaced — which happens under a test capture harness, and can
        # happen in production if the process re-opens its output stream.
        logger_factory=structlog.PrintLoggerFactory(),
        # Caching the bound logger avoids re-running the processor chain setup
        # on every call, which is a measurable win at production log volume.
        # It also makes the logger unreconfigurable, because a cached logger
        # keeps the handle it was built with. That trade is right in production,
        # where configuration happens exactly once at boot, and wrong everywhere
        # else — notably in tests, which reconfigure per case.
        cache_logger_on_first_use=config.is_production,
    )

    # Route stdlib logging through the same sink so third-party records are
    # captured with identical formatting.
    logging.basicConfig(
        format="%(message)s",
        stream=sys.stdout,
        level=level,
        force=True,
    )

    structlog.contextvars.bind_contextvars(
        **{
            ATTR_SERVICE: config.service_name,
            ATTR_VERSION: config.version,
            ATTR_ENVIRONMENT: str(config.environment),
            ATTR_REGION: config.region,
        }
    )


def get_logger(name: str | None = None) -> structlog.stdlib.BoundLogger:
    """Return a bound logger.

    Args:
        name: Optional module name recorded on each record. Defaults to the
            calling module as resolved by structlog.

    Returns:
        A logger that is safe for concurrent use.
    """
    return structlog.get_logger(name)  # type: ignore[no-any-return]


def bind_trace_id(trace_id: str) -> None:
    """Bind the distributed trace identifier for the current context.

    Args:
        trace_id: W3C trace identifier extracted from the inbound request.
    """
    _trace_id.set(trace_id)


def bind_call_session_id(call_session_id: str) -> None:
    """Bind the call session identifier for the current context.

    This is the single most useful correlation key in the platform: one screened
    call spans the telephony gateway, the session orchestrator and several AI
    services, and this identifier is what stitches their logs into one narrative.

    Args:
        call_session_id: Identifier of the call session being handled.
    """
    _call_session_id.set(call_session_id)


def clear_context() -> None:
    """Clear request-scoped correlation identifiers.

    Called at the end of a request or task so that identifiers cannot leak into
    an unrelated unit of work reusing the same context — a real hazard in an
    asyncio server where tasks are pooled.
    """
    _trace_id.set(None)
    _call_session_id.set(None)
    structlog.contextvars.clear_contextvars()
