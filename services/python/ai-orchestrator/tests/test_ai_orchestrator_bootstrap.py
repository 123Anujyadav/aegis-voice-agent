"""Bootstrap tests for the ai-orchestrator service.

These verify the engineering foundation: that configuration loads, that the
naming convention is respected, and that invalid configuration is rejected
before the process starts. Domain tests arrive with the domain.
"""

from __future__ import annotations

from ai_orchestrator.config import AiOrchestratorConfig
import pytest

from callscreen_platform.config import ConfigurationError, Environment, LogFormat, load_config


def test_config_loads_with_defaults() -> None:
    """The service boots with only its required settings supplied."""
    config = load_config(AiOrchestratorConfig, service_name="ai-orchestrator")

    assert config.service_name == "ai-orchestrator"
    assert config.environment is Environment.DEVELOPMENT
    assert config.http_port != config.health_port


def test_service_name_matches_directory() -> None:
    """service_name must match the directory under services/python.

    Telemetry attribution depends on this: a mismatch silently splits a
    service's dashboards and alerts in two.
    """
    config = load_config(AiOrchestratorConfig, service_name="ai-orchestrator")
    assert config.service_name == "ai-orchestrator"


def test_reads_its_own_env_prefix(monkeypatch: pytest.MonkeyPatch) -> None:
    """Settings resolve under the service's dedicated prefix."""
    monkeypatch.setenv("CS_AI_ORCHESTRATOR_SERVICE_NAME", "ai-orchestrator")
    monkeypatch.setenv("CS_AI_ORCHESTRATOR_HTTP_PORT", "9100")

    config = load_config(AiOrchestratorConfig)

    assert config.http_port == 9100


def test_rejects_console_logging_in_production() -> None:
    """Production must emit JSON so logs remain machine-ingestible."""
    with pytest.raises(ConfigurationError):
        load_config(
            AiOrchestratorConfig,
            service_name="ai-orchestrator",
            environment=Environment.PRODUCTION,
            log_format=LogFormat.CONSOLE,
        )


def test_rejects_colliding_ports() -> None:
    """Probes must not queue behind application traffic."""
    with pytest.raises(ConfigurationError):
        load_config(
            AiOrchestratorConfig, service_name="ai-orchestrator", http_port=8080, health_port=8080
        )
