from pydantic import Field

from .base_model import BaseModel
from .fragments import (
    OrchestratorsConnectionDefault,
    OrchestratorsConnectionDefaultNodes,  # noqa: F401
)


class GetOrchestratorsConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    orchestrators_connection: "GetOrchestratorsConnectionOrchestratorsConnection" = Field(
        alias="orchestratorsConnection",
        description="List orchestrators discovered by Livepeer gateways under this cluster owner\ntenant. Federation-map data source. Vantage-independent; the per-region\ntable comes from `orchestratorVantages`.",
    )
    "List orchestrators discovered by Livepeer gateways under this cluster owner\ntenant. Federation-map data source. Vantage-independent; the per-region\ntable comes from `orchestratorVantages`."


class GetOrchestratorsConnectionOrchestratorsConnection(OrchestratorsConnectionDefault):
    """Pagination wrapper for orchestrator listing."""

    pass


GetOrchestratorsConnection.model_rebuild()
