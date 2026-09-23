from pydantic import Field

from .base_model import BaseModel
from .fragments import ClusterDefault, PageInfoDefault


class GetClustersConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    clusters_connection: "GetClustersConnectionClustersConnection" = Field(
        alias="clustersConnection",
        description="List clusters the tenant has access to.",
    )
    "List clusters the tenant has access to."


class GetClustersConnectionClustersConnection(BaseModel):
    edges: list["GetClustersConnectionClustersConnectionEdges"]
    page_info: "GetClustersConnectionClustersConnectionPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


class GetClustersConnectionClustersConnectionEdges(BaseModel):
    cursor: str
    node: "GetClustersConnectionClustersConnectionEdgesNode"


class GetClustersConnectionClustersConnectionEdgesNode(ClusterDefault):
    """A streaming cluster containing one or more infrastructure nodes.
    Clusters provide isolated capacity for streaming workloads."""

    pass


GetClustersConnectionClustersConnectionPageInfo = PageInfoDefault
GetClustersConnection.model_rebuild()
GetClustersConnectionClustersConnection.model_rebuild()
GetClustersConnectionClustersConnectionEdges.model_rebuild()
