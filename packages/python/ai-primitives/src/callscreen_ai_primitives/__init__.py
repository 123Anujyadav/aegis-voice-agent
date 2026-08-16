"""Shared primitives for the AI tier: token accounting, provider failover and cost metering.

Admitted to packages/ under Phase 1 §4 because cost control and vendor failover
are cross-cutting concerns of the AI tier specifically, and reimplementing them
per service would produce divergent cost accounting across the six services that
call model providers.

Phase 2 scope: package skeleton and version marker only. The primitives
themselves are implemented alongside the first service that needs them.
"""

__version__ = "0.1.0"
