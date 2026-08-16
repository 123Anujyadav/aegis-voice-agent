"""Typed configuration loading with fail-fast validation at process boot.

This implements the environment-variable strategy from Phase 1 §11 for the
Python tier, matching the behaviour of ``packages/go/platform/config.go`` field
for field.

The central design rule is that a service with invalid configuration **refuses
to start**. A service that starts on a silent default is more dangerous than one
that does not start at all, because the resulting misbehaviour surfaces later,
further from its cause, and often only under load.
"""

from __future__ import annotations

from enum import StrEnum
import os
from typing import Any, Self

from pydantic import Field, ValidationError, field_validator, model_validator
from pydantic_settings import BaseSettings, SettingsConfigDict


class ConfigurationError(RuntimeError):
    """Raised when configuration is absent or invalid.

    The message aggregates every problem discovered rather than reporting only
    the first. An operator correcting a broken deployment needs the complete
    list; discovering one missing variable per restart cycle turns a two-minute
    fix into a twenty-minute one during an incident.
    """

    def __init__(self, problems: list[str]) -> None:
        """Build an aggregate error.

        Args:
            problems: One human-readable description per invalid or missing
                setting. Rendered one per line so the message is readable in a
                container log.
        """
        self.problems = problems
        detail = "\n".join(f"  - {p}" for p in problems)
        super().__init__(f"configuration invalid ({len(problems)} problem(s)):\n{detail}")


class Environment(StrEnum):
    """Deployment tier.

    Behaviour that must never be enabled in production — verbose error bodies,
    permissive CORS, seeded fixtures — gates on this value.
    """

    DEVELOPMENT = "development"
    STAGING = "staging"
    PRODUCTION = "production"


class LogLevel(StrEnum):
    """Minimum severity emitted by the logger."""

    DEBUG = "debug"
    INFO = "info"
    WARN = "warn"
    ERROR = "error"


class LogFormat(StrEnum):
    """Log rendering format.

    ``JSON`` is required in production for machine ingestion. ``CONSOLE`` is a
    human-readable development affordance and is rejected in production by
    :meth:`ServiceConfig.validate_production_invariants`.
    """

    JSON = "json"
    CONSOLE = "console"


class ServiceConfig(BaseSettings):
    """Configuration block embedded by every Python service.

    It exists so that operational behaviour — port binding, log verbosity,
    shutdown grace, environment identity — is identical across all six Python
    services. Divergence in these settings is a recurring source of confusion
    during incidents, where an engineer assumes one service behaves like its
    neighbour and it does not.

    Values are read from environment variables prefixed ``CS_``. A service
    supplies its own prefix by subclassing and overriding ``model_config``; see
    ``services/python/*/src/*/config.py`` for the pattern.

    Attributes mirror ``platform.ServiceConfig`` in the Go module exactly. When
    changing one, change the other in the same pull request.
    """

    model_config = SettingsConfigDict(
        env_prefix="CS_",
        # Values arrive from the environment, never from a committed .env file.
        # A .env in a deployed environment is a Phase 1 §11 violation; it is
        # permitted only for local development, where the developer creates it.
        env_file=None,
        case_sensitive=False,
        # Reject unknown CS_-prefixed variables. A typo such as CS_LOG_LEVL
        # would otherwise be silently ignored and the service would run at the
        # default verbosity while the operator believed otherwise.
        extra="forbid",
        frozen=True,
        validate_default=True,
    )

    service_name: str = Field(
        ...,
        min_length=1,
        description=(
            "Service identifier, matching its directory under services/python. "
            "Attached to every log record, metric and trace span."
        ),
    )

    environment: Environment = Field(
        default=Environment.DEVELOPMENT,
        description="Deployment tier gating environment-specific behaviour.",
    )

    region: str = Field(
        default="in-south-1",
        min_length=1,
        description=(
            "Deployment region. Recorded on telemetry so that data residency "
            "can be audited from logs alone (Phase 1 assumption A2)."
        ),
    )

    version: str = Field(
        default="dev",
        min_length=1,
        description=(
            "Build version injected at container build time. Used to attribute "
            "error-rate changes to a specific rollout."
        ),
    )

    http_port: int = Field(
        default=8080,
        ge=1,
        le=65535,
        description="Port the application HTTP listener binds.",
    )

    health_port: int = Field(
        default=8081,
        ge=1,
        le=65535,
        description=(
            "Separate port for liveness and readiness probes. Deliberately "
            "distinct from http_port so probes keep answering when the main "
            "listener is saturated or draining — a health check queued behind "
            "application traffic reports the wrong answer precisely when the "
            "answer matters most."
        ),
    )

    log_level: LogLevel = Field(
        default=LogLevel.INFO,
        description="Minimum severity emitted.",
    )

    log_format: LogFormat = Field(
        default=LogFormat.JSON,
        description="Log rendering format. Must be JSON in production.",
    )

    shutdown_timeout_seconds: float = Field(
        default=25.0,
        gt=0,
        le=300,
        description=(
            "Upper bound on graceful shutdown. Must be shorter than the "
            "orchestrator's termination grace period, otherwise the process is "
            "killed mid-drain and the graceful path never completes."
        ),
    )

    request_timeout_seconds: float = Field(
        default=30.0,
        gt=0,
        le=600,
        description=(
            "Default upper bound on an inbound request. Every request is "
            "bounded; an unbounded request in a realtime voice system is an "
            "outage waiting for enough concurrency to trigger it."
        ),
    )

    log_hash_key: str = Field(
        default="",
        description=(
            "Key for the HMAC used to pseudonymise personal identifiers in "
            "logs. Sourced from the secret manager in deployed environments. "
            "When empty a random per-process key is generated, which confines "
            "correlation to a single process."
        ),
        repr=False,
    )

    @field_validator("service_name")
    @classmethod
    def _validate_service_name(cls, value: str) -> str:
        """Enforce the kebab-case naming convention from Phase 1 §5.

        Args:
            value: Proposed service name.

        Returns:
            The validated name.

        Raises:
            ValueError: If the name is not lowercase kebab-case.
        """
        if not value.replace("-", "").isalnum() or value != value.lower():
            msg = (
                f"service_name must be lowercase kebab-case matching the "
                f"directory under services/python, got {value!r}"
            )
            raise ValueError(msg)
        return value

    @model_validator(mode="after")
    def validate_production_invariants(self) -> Self:
        """Enforce invariants that cannot be expressed on a single field.

        Returns:
            The validated configuration.

        Raises:
            ValueError: If ports collide, or if a production deployment is
                configured with console logging. Console logging in production
                defeats log ingestion and is almost always an accident
                inherited from a copied local configuration.
        """
        if self.http_port == self.health_port:
            msg = f"http_port and health_port must differ, both are {self.http_port}"
            raise ValueError(msg)

        if self.environment is Environment.PRODUCTION and self.log_format is not LogFormat.JSON:
            msg = "log_format must be json in production"
            raise ValueError(msg)

        return self

    @property
    def is_production(self) -> bool:
        """Whether the service runs in the production tier.

        Returns:
            ``True`` when the environment is production.
        """
        return self.environment is Environment.PRODUCTION


def _detect_unknown_env_vars(config_type: type[ServiceConfig]) -> list[str]:
    """Find environment variables using the model's prefix but naming no field.

    Pydantic's ``extra="forbid"`` governs values passed explicitly to the
    constructor; it does **not** apply to environment scanning, because
    pydantic-settings only ever looks up the variables it knows about and never
    enumerates the environment. A typo such as ``CS_EDGE_API_LOG_LEVL`` is
    therefore invisible to it: the variable is simply never read, the service
    starts at the default verbosity, and the operator believes otherwise.

    Closing that gap requires scanning the environment ourselves, which is what
    this function does. It is the difference between a misconfiguration that
    announces itself at boot and one that is discovered during an incident.

    Args:
        config_type: The configuration model whose prefix and fields define
            what is legitimate.

    Returns:
        Sorted names of prefixed variables that match no field on the model.
    """
    prefix = str(config_type.model_config.get("env_prefix", "")).upper()
    if not prefix:
        return []

    known = set()
    for name, field in config_type.model_fields.items():
        known.add(name.upper())
        if field.alias:
            known.add(field.alias.upper())
        if field.validation_alias and isinstance(field.validation_alias, str):
            known.add(field.validation_alias.upper())

    return sorted(
        key
        for key in os.environ
        if key.upper().startswith(prefix) and key.upper().removeprefix(prefix) not in known
    )


def load_config[ConfigT: ServiceConfig](
    config_type: type[ConfigT],
    **overrides: Any,  # noqa: ANN401 -- pydantic accepts arbitrary field values
) -> ConfigT:
    """Load and validate configuration, or fail loudly.

    This is the only supported way to construct a service configuration. It
    converts Pydantic's ``ValidationError`` into a :class:`ConfigurationError`
    whose message lists every problem at once, matching the behaviour of
    ``LoadConfig`` in the Go platform module, and additionally rejects
    prefixed environment variables that name no field.

    Args:
        config_type: The concrete ``ServiceConfig`` subclass to instantiate.
        **overrides: Explicit values that take precedence over the environment.
            Used by tests to construct a valid configuration without mutating
            process-wide state.

    Returns:
        A validated, immutable configuration instance.

    Raises:
        ConfigurationError: If any setting is missing, invalid, or unrecognised.
            The service must not continue past this point.
    """
    problems: list[str] = []

    # Unknown-variable detection is skipped when the caller supplies explicit
    # overrides, because a test constructing a configuration directly has no
    # reason to be constrained by whatever happens to be in the ambient
    # environment of the machine running it.
    if not overrides:
        problems.extend(
            f"{name}: unrecognised setting; no such field on "
            f"{config_type.__name__} (check for a typo)"
            for name in _detect_unknown_env_vars(config_type)
        )

    try:
        config = config_type(**overrides)
    except ValidationError as exc:
        for error in exc.errors():
            location = ".".join(str(part) for part in error["loc"]) or "<root>"
            problems.append(f"{location}: {error['msg']}")
        raise ConfigurationError(sorted(problems)) from exc

    if problems:
        raise ConfigurationError(sorted(problems))

    return config
