"""Tests for typed configuration loading and fail-fast validation."""

from __future__ import annotations

from pydantic_settings import SettingsConfigDict
import pytest

from callscreen_platform.config import (
    ConfigurationError,
    Environment,
    LogFormat,
    LogLevel,
    ServiceConfig,
    load_config,
)


class SampleConfig(ServiceConfig):
    """Concrete configuration used by these tests.

    ``ServiceConfig`` is abstract in practice: every service subclasses it to
    set its own environment prefix. Using a dedicated subclass here keeps the
    tests isolated from any real service's variable namespace.
    """

    model_config = SettingsConfigDict(
        env_prefix="CS_TEST_",
        env_file=None,
        case_sensitive=False,
        extra="forbid",
        frozen=True,
    )


def test_loads_from_environment(monkeypatch: pytest.MonkeyPatch) -> None:
    """Values present in the environment populate the model."""
    monkeypatch.setenv("CS_TEST_SERVICE_NAME", "fraud-engine")
    monkeypatch.setenv("CS_TEST_ENVIRONMENT", "staging")
    monkeypatch.setenv("CS_TEST_HTTP_PORT", "9090")
    monkeypatch.setenv("CS_TEST_LOG_LEVEL", "debug")

    config = load_config(SampleConfig)

    assert config.service_name == "fraud-engine"
    assert config.environment is Environment.STAGING
    assert config.http_port == 9090
    assert config.log_level is LogLevel.DEBUG


def test_defaults_applied_when_unset(monkeypatch: pytest.MonkeyPatch) -> None:
    """Unset settings fall back to their declared defaults."""
    monkeypatch.setenv("CS_TEST_SERVICE_NAME", "asr-gateway")

    config = load_config(SampleConfig)

    assert config.environment is Environment.DEVELOPMENT
    assert config.http_port == 8080
    assert config.health_port == 8081
    assert config.region == "in-south-1"
    assert config.log_format is LogFormat.JSON


def test_missing_required_setting_raises(monkeypatch: pytest.MonkeyPatch) -> None:
    """A service with no service_name refuses to start.

    This is the core fail-fast contract: starting on a silent default is more
    dangerous than not starting, because the misbehaviour surfaces later and
    further from its cause.
    """
    monkeypatch.delenv("CS_TEST_SERVICE_NAME", raising=False)

    with pytest.raises(ConfigurationError) as excinfo:
        load_config(SampleConfig)

    assert "service_name" in str(excinfo.value)


def test_error_aggregates_every_problem(monkeypatch: pytest.MonkeyPatch) -> None:
    """Every fault is reported at once, not one per restart cycle."""
    monkeypatch.delenv("CS_TEST_SERVICE_NAME", raising=False)
    monkeypatch.setenv("CS_TEST_HTTP_PORT", "not-a-port")
    monkeypatch.setenv("CS_TEST_LOG_LEVEL", "verbose")

    with pytest.raises(ConfigurationError) as excinfo:
        load_config(SampleConfig)

    message = str(excinfo.value)
    assert "3 problem(s)" in message
    assert "service_name" in message
    assert "http_port" in message
    assert "log_level" in message


def test_unknown_setting_rejected(monkeypatch: pytest.MonkeyPatch) -> None:
    """A typo in a variable name fails loudly rather than being ignored.

    Without ``extra="forbid"``, ``CS_TEST_LOG_LEVL`` would be silently dropped
    and the service would run at default verbosity while the operator believed
    otherwise.
    """
    monkeypatch.setenv("CS_TEST_SERVICE_NAME", "billing")
    monkeypatch.setenv("CS_TEST_LOG_LEVL", "debug")

    with pytest.raises(ConfigurationError):
        load_config(SampleConfig)


@pytest.mark.parametrize(
    "name",
    ["Edge-API", "edge_api", "edge api", "EDGEAPI", ""],
)
def test_rejects_non_kebab_case_service_name(name: str) -> None:
    """service_name must match the directory naming convention (Phase 1 §5)."""
    with pytest.raises(ConfigurationError):
        load_config(SampleConfig, service_name=name)


@pytest.mark.parametrize("name", ["edge-api", "billing", "asr-gateway", "tts2"])
def test_accepts_valid_service_names(name: str) -> None:
    """Lowercase kebab-case names are accepted."""
    config = load_config(SampleConfig, service_name=name)
    assert config.service_name == name


def test_colliding_ports_rejected() -> None:
    """http_port and health_port must differ.

    Binding both to the same port means probes queue behind application traffic,
    which is the exact failure the split is designed to prevent.
    """
    with pytest.raises(ConfigurationError) as excinfo:
        load_config(SampleConfig, service_name="edge-api", http_port=8080, health_port=8080)

    assert "must differ" in str(excinfo.value)


def test_console_logging_rejected_in_production() -> None:
    """Console logging in production defeats log ingestion."""
    with pytest.raises(ConfigurationError) as excinfo:
        load_config(
            SampleConfig,
            service_name="edge-api",
            environment=Environment.PRODUCTION,
            log_format=LogFormat.CONSOLE,
        )

    assert "json in production" in str(excinfo.value)


def test_json_logging_accepted_in_production() -> None:
    """The production invariant permits the correct configuration."""
    config = load_config(
        SampleConfig,
        service_name="edge-api",
        environment=Environment.PRODUCTION,
        log_format=LogFormat.JSON,
    )

    assert config.is_production is True


@pytest.mark.parametrize(
    ("environment", "expected"),
    [
        (Environment.PRODUCTION, True),
        (Environment.STAGING, False),
        (Environment.DEVELOPMENT, False),
    ],
)
def test_is_production(environment: Environment, expected: bool) -> None:
    """Security-relevant behaviour gates on this property, so it is pinned."""
    config = load_config(SampleConfig, service_name="edge-api", environment=environment)
    assert config.is_production is expected


@pytest.mark.parametrize("port", [0, -1, 65536, 100000])
def test_port_range_enforced(port: int) -> None:
    """Ports outside the valid TCP range are rejected at boot."""
    with pytest.raises(ConfigurationError):
        load_config(SampleConfig, service_name="edge-api", http_port=port)


@pytest.mark.parametrize("timeout", [0, -1.0, 301.0])
def test_shutdown_timeout_bounds_enforced(timeout: float) -> None:
    """A non-positive or absurdly long shutdown budget is a configuration bug."""
    with pytest.raises(ConfigurationError):
        load_config(SampleConfig, service_name="edge-api", shutdown_timeout_seconds=timeout)


def test_config_is_immutable() -> None:
    """Configuration cannot be mutated after validation.

    Mutable configuration invites a component to change a setting at runtime,
    producing behaviour that cannot be reproduced from the deployment manifest.

    Pydantic raises ``ValidationError`` (a ``ValueError`` subclass) on
    assignment to a frozen model. ``AttributeError`` is accepted alongside it so
    the test asserts the intent — "this must not be assignable" — rather than
    coupling to a library implementation detail.
    """
    config = load_config(SampleConfig, service_name="edge-api")

    with pytest.raises((ValueError, AttributeError)):
        config.http_port = 9999


def test_log_hash_key_excluded_from_repr() -> None:
    """The redaction key must not appear in a repr captured by an error report."""
    config = load_config(SampleConfig, service_name="edge-api", log_hash_key="super-secret-key")

    assert "super-secret-key" not in repr(config)
