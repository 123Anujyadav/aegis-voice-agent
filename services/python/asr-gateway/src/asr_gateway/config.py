"""Configuration for the asr-gateway service."""

from __future__ import annotations

from pydantic_settings import SettingsConfigDict

from callscreen_platform.config import ServiceConfig


class AsrGatewayConfig(ServiceConfig):
    """Settings for asr-gateway.

    Inherits every operational setting from :class:ServiceConfig so that port
    binding, log verbosity and shutdown grace behave identically across all
    fourteen services. Service-specific settings are added here as the domain
    is implemented in later phases.

    Variables are read with the `CS_ASR_GATEWAY_` prefix, matching the
    naming convention in Phase 1 §5.
    """

    model_config = SettingsConfigDict(
        env_prefix="CS_ASR_GATEWAY_",
        env_file=None,
        case_sensitive=False,
        extra="forbid",
        frozen=True,
    )
