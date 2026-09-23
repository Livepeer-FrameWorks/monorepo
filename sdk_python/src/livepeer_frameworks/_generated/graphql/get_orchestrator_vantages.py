from pydantic import Field

from .base_model import BaseModel
from .fragments import OrchestratorVantageDefault


class GetOrchestratorVantages(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    orchestrator_vantages: list["GetOrchestratorVantagesOrchestratorVantages"] = Field(
        alias="orchestratorVantages",
        description="List per-(gateway, instance) vantage observations. Multi-region observation\nsurfaces here as multiple rows. Use the optional `orchAddr` filter to\nscope to one orchestrator.",
    )
    "List per-(gateway, instance) vantage observations. Multi-region observation\nsurfaces here as multiple rows. Use the optional `orchAddr` filter to\nscope to one orchestrator."


class GetOrchestratorVantagesOrchestratorVantages(OrchestratorVantageDefault):
    """Per-vantage observation: one row per (cluster owner tenant, gateway, orch
    address, resolved IP). DNS round-robin / geo-anycast surfaces as multiple
    vantages with different `resolvedIp`."""

    pass


GetOrchestratorVantages.model_rebuild()
