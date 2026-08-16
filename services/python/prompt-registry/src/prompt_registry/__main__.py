"""Process entrypoint for the prompt-registry service."""

from __future__ import annotations

import sys

from callscreen_platform.config import load_config
from callscreen_platform.runtime import run_service
from prompt_registry.config import PromptRegistryConfig


def main() -> int:
    """Boot the service.

    Loads and validates configuration, configures structured logging, and runs
    the lifecycle until SIGTERM. Configuration failure exits 78 (`EX_CONFIG`)
    without starting, per the fail-fast contract in Phase 1 §11.

    Returns:
        A process exit code.
    """
    return run_service(lambda: load_config(PromptRegistryConfig))


if __name__ == "__main__":
    sys.exit(main())
