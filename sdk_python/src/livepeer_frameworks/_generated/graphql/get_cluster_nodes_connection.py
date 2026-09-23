from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import (  # noqa: F401
    InfrastructureNodeDefault,
    InfrastructureNodeDefaultLiveState,
    InfrastructureNodeDefaultRoutingImpactPreview,
    PageInfoDefault,
)


class GetClusterNodesConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    cluster: Optional["GetClusterNodesConnectionCluster"] = Field(
        description="Fetch a single cluster by ID."
    )
    "Fetch a single cluster by ID."


class GetClusterNodesConnectionCluster(BaseModel):
    """A streaming cluster containing one or more infrastructure nodes.
    Clusters provide isolated capacity for streaming workloads."""

    nodes_connection: Optional["GetClusterNodesConnectionClusterNodesConnection"] = (
        Field(
            alias="nodesConnection",
            description="Paginated list of nodes in this cluster.",
        )
    )
    "Paginated list of nodes in this cluster."


class GetClusterNodesConnectionClusterNodesConnection(BaseModel):
    edges: list["GetClusterNodesConnectionClusterNodesConnectionEdges"]
    page_info: "GetClusterNodesConnectionClusterNodesConnectionPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


class GetClusterNodesConnectionClusterNodesConnectionEdges(BaseModel):
    cursor: str
    node: "GetClusterNodesConnectionClusterNodesConnectionEdgesNode"


class GetClusterNodesConnectionClusterNodesConnectionEdgesNode(
    InfrastructureNodeDefault
):
    """An infrastructure node in a cluster (edge server, origin, transcoder).
    Nodes handle stream ingest, transcoding, and delivery to viewers."""

    pass


GetClusterNodesConnectionClusterNodesConnectionPageInfo = PageInfoDefault
GetClusterNodesConnection.model_rebuild()
GetClusterNodesConnectionCluster.model_rebuild()
GetClusterNodesConnectionClusterNodesConnection.model_rebuild()
GetClusterNodesConnectionClusterNodesConnectionEdges.model_rebuild()
