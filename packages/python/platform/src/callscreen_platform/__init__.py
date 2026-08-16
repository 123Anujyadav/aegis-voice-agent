"""Cross-cutting infrastructure shared by every CallScreen Python service.

This package is the Python counterpart of ``packages/go/platform`` and is kept
semantically identical to it: the same configuration precedence, the same log
field names, and the same liveness/readiness distinction. That symmetry is a
deliberate requirement from Phase 1 §4 — during an incident an engineer must be
able to run a single query across both the Go and Python tiers, which is
impossible if each tier names its fields differently.

What belongs here
-----------------
Configuration loading, structured logging with automatic redaction, health
reporting, and lifecycle management. Nothing else.

What does not belong here
-------------------------
Business logic of any kind, domain models, and anything specific to a single
service. Phase 1 §4 governs admission: a candidate must be cross-cutting
infrastructure, contain no business meaning, and have at least two real
consumers today.

Public surface
--------------
Everything re-exported below is the supported API. Anything reached through a
module path not listed here is internal and may change without notice.
"""

from callscreen_platform.config import (
    ConfigurationError,
    Environment,
    LogFormat,
    LogLevel,
    ServiceConfig,
)
from callscreen_platform.health import (
    CheckResult,
    Health,
    HealthStatus,
)
from callscreen_platform.logging import (
    ATTR_CALL_SESSION_ID,
    ATTR_DURATION_MS,
    ATTR_ENVIRONMENT,
    ATTR_ERROR,
    ATTR_REGION,
    ATTR_SERVICE,
    ATTR_TRACE_ID,
    ATTR_VERSION,
    bind_call_session_id,
    bind_trace_id,
    clear_context,
    configure_logging,
    get_logger,
)

__all__ = [
    "ATTR_CALL_SESSION_ID",
    "ATTR_DURATION_MS",
    "ATTR_ENVIRONMENT",
    "ATTR_ERROR",
    "ATTR_REGION",
    "ATTR_SERVICE",
    "ATTR_TRACE_ID",
    "ATTR_VERSION",
    "CheckResult",
    "ConfigurationError",
    "Environment",
    "Health",
    "HealthStatus",
    "LogFormat",
    "LogLevel",
    "ServiceConfig",
    "bind_call_session_id",
    "bind_trace_id",
    "clear_context",
    "configure_logging",
    "get_logger",
]

__version__ = "0.1.0"
