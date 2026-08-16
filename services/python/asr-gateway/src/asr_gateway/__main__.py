"""Process entrypoint for the asr-gateway service."""

from __future__ import annotations

import sys

from asr_gateway.config import AsrGatewayConfig
from callscreen_platform.config import load_config
from callscreen_platform.runtime import run_service


def main() -> int:
    """Boot the service.

    Loads and validates configuration, configures structured logging, and runs
    the lifecycle until SIGTERM. Configuration failure exits 78 (`EX_CONFIG`)
    without starting, per the fail-fast contract in Phase 1 §11.

    Returns:
        A process exit code.
    """
    return run_service(lambda: load_config(AsrGatewayConfig))


if __name__ == "__main__":
    sys.exit(main())
