from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import ClusterDefault


class GetCluster(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    cluster: Optional["GetClusterCluster"] = Field(
        description="Fetch a single cluster by ID."
    )
    "Fetch a single cluster by ID."


class GetClusterCluster(ClusterDefault):
    """A streaming cluster containing one or more infrastructure nodes.
    Clusters provide isolated capacity for streaming workloads."""

    pass


GetCluster.model_rebuild()
