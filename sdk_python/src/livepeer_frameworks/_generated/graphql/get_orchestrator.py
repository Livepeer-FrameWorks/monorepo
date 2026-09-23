from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import (  # noqa: F401
    OrchestratorWithDetailsDefault,
    OrchestratorWithDetailsDefaultInstances,
    OrchestratorWithDetailsDefaultOrchestrator,
    OrchestratorWithDetailsDefaultVantages,
)


class GetOrchestrator(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    orchestrator: Optional["GetOrchestratorOrchestrator"] = Field(
        description="Fetch a public orchestrator with every known instance and per-(gateway,\ninstance) vantage. Side-panel data source for the federation map."
    )
    "Fetch a public orchestrator with every known instance and per-(gateway,\ninstance) vantage. Side-panel data source for the federation map."


class GetOrchestratorOrchestrator(OrchestratorWithDetailsDefault):
    """Detail response: orchestrator identity + every known instance (with their
    own price/capabilities/hardware) + every per-(gateway, instance) vantage.
    Used by the federation map's side panel."""

    pass


GetOrchestrator.model_rebuild()
