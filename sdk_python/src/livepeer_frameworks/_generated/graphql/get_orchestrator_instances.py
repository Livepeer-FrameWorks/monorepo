from pydantic import Field

from .base_model import BaseModel
from .fragments import (
    OrchestratorInstanceDefault,
    OrchestratorInstanceDefaultCapabilityPrices,  # noqa: F401
)


class GetOrchestratorInstances(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    orchestrator_instances: list["GetOrchestratorInstancesOrchestratorInstances"] = (
        Field(
            alias="orchestratorInstances",
            description="List public per-instance rows. Each carries its own\nprice/capabilities/hardware — usually consistent across an orch's pool but\nnot guaranteed. Use the optional `orchAddr` filter to scope to one orch.",
        )
    )
    "List public per-instance rows. Each carries its own\nprice/capabilities/hardware — usually consistent across an orch's pool but\nnot guaranteed. Use the optional `orchAddr` filter to scope to one orch."


class GetOrchestratorInstancesOrchestratorInstances(OrchestratorInstanceDefault):
    """Per-instance state. One per (tenant, orch_addr, resolved_ip). Multiple
    instances of the same orch can have independent prices, capabilities, and
    hardware — the side panel renders this list so divergence is visible."""

    pass


GetOrchestratorInstances.model_rebuild()
