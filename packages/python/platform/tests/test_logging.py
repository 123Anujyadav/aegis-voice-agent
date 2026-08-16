"""Tests for structured logging and the mandatory redaction policy.

These are security regression tests. A failure here means personal data or
credentials can reach a log sink, which is a reportable incident under the DPDP
Act, not merely a bug.
"""

from __future__ import annotations

import json
from typing import Any

import pytest

from callscreen_platform.config import LogFormat, LogLevel, ServiceConfig
from callscreen_platform.logging import (
    ATTR_CALL_SESSION_ID,
    ATTR_SERVICE,
    ATTR_TRACE_ID,
    bind_call_session_id,
    bind_trace_id,
    clear_context,
    configure_logging,
    get_logger,
)


@pytest.fixture(autouse=True)
def _reset_context() -> Any:
    """Clear correlation identifiers around every test.

    Without this, a bound trace id leaks into the next test through the shared
    ContextVar and produces order-dependent failures — which pytest-randomly
    would surface at random, wasting debugging time.
    """
    clear_context()
    yield
    clear_context()


@pytest.fixture
def configured(capsys: pytest.CaptureFixture[str]) -> Any:
    """Configure logging with a stable hash key and return a capture helper."""
    config = ServiceConfig(
        service_name="test-service",
        version="1.2.3",
        log_level=LogLevel.DEBUG,
        log_format=LogFormat.JSON,
        log_hash_key="deterministic-test-key",
    )
    configure_logging(config)

    def read_records() -> list[dict[str, Any]]:
        """Parse every JSON record written to stdout since the last read."""
        captured = capsys.readouterr().out
        return [json.loads(line) for line in captured.splitlines() if line.strip()]

    return read_records


def test_identity_fields_attached_to_every_record(configured: Any) -> None:
    """Service, version, environment and region appear without being restated.

    This is what makes logs from fourteen services queryable as one stream.
    """
    get_logger().info("something happened")

    records = configured()
    assert len(records) == 1
    assert records[0][ATTR_SERVICE] == "test-service"
    assert records[0]["version"] == "1.2.3"
    assert records[0]["region"] == "in-south-1"


@pytest.mark.parametrize(
    "key",
    [
        "password",
        "api_key",
        "authorization",
        "access_token",
        "refresh_token",
        "otp",
        "cvv",
        "attestation",
        "x-authorization",
        "AUTHORIZATION",
        "user_password",
    ],
)
def test_sensitive_keys_are_dropped(configured: Any, key: str) -> None:
    """Credential-bearing attributes never reach the sink verbatim."""
    get_logger().info("auth attempt", **{key: "the-real-secret-value"})

    records = configured()
    assert records[0][key] == "[REDACTED]"
    assert "the-real-secret-value" not in json.dumps(records[0])


@pytest.mark.parametrize(
    "key",
    ["phone", "msisdn", "caller_number", "email", "install_id", "source_ip"],
)
def test_personal_identifiers_are_pseudonymised(configured: Any, key: str) -> None:
    """Personal identifiers are hashed, not dropped.

    Dropping them would destroy the ability to correlate two records about the
    same caller, which is operationally necessary. Hashing preserves
    correlation without exposing the value.
    """
    get_logger().info("call received", **{key: "+919876543210"})

    records = configured()
    value = records[0][key]
    assert value.startswith("pseudo:")
    assert "+919876543210" not in json.dumps(records[0])


def test_pseudonyms_are_stable_within_a_process(configured: Any) -> None:
    """The same input yields the same pseudonym, so records remain joinable."""
    logger = get_logger()
    logger.info("first", caller_number="+919876543210")
    logger.info("second", caller_number="+919876543210")
    logger.info("third", caller_number="+919000000000")

    records = configured()
    assert records[0]["caller_number"] == records[1]["caller_number"]
    assert records[0]["caller_number"] != records[2]["caller_number"]


def test_redaction_recurses_into_nested_structures(configured: Any) -> None:
    """Nesting cannot be used to smuggle a sensitive value past the filter."""
    get_logger().info(
        "request",
        payload={
            "user": {"email": "someone@example.com", "password": "hunter2"},
            "tokens": ["a", "b"],
        },
    )

    serialised = json.dumps(configured()[0])
    assert "hunter2" not in serialised
    assert "someone@example.com" not in serialised


def test_trace_id_promoted_from_context(configured: Any) -> None:
    """Correlation identifiers attach automatically once bound."""
    bind_trace_id("trace-abc-123")
    get_logger().info("handled")

    assert configured()[0][ATTR_TRACE_ID] == "trace-abc-123"


def test_call_session_id_promoted_from_context(configured: Any) -> None:
    """The call session id is the key that stitches a call across services."""
    bind_call_session_id("cs-session-987")
    get_logger().info("turn complete")

    assert configured()[0][ATTR_CALL_SESSION_ID] == "cs-session-987"


def test_clear_context_removes_identifiers(configured: Any) -> None:
    """Identifiers must not leak into an unrelated task reusing the context."""
    bind_trace_id("trace-abc-123")
    clear_context()
    get_logger().info("unrelated work")

    assert ATTR_TRACE_ID not in configured()[0]


def test_level_filtering_suppresses_below_threshold(
    capsys: pytest.CaptureFixture[str],
) -> None:
    """Records below the configured level are not emitted."""
    configure_logging(
        ServiceConfig(
            service_name="test-service",
            log_level=LogLevel.WARN,
            log_format=LogFormat.JSON,
        )
    )

    logger = get_logger()
    logger.debug("invisible")
    logger.info("also invisible")
    logger.warning("visible")

    lines = [ln for ln in capsys.readouterr().out.splitlines() if ln.strip()]
    assert len(lines) == 1
    assert json.loads(lines[0])["event"] == "visible"
