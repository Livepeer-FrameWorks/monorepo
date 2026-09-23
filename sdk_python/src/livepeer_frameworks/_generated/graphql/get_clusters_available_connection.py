from pydantic import Field

from .base_model import BaseModel
from .fragments import AvailableClusterDefault, PageInfoDefault


class GetClustersAvailableConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    clusters_available_connection: "GetClustersAvailableConnectionClustersAvailableConnection" = Field(
        alias="clustersAvailableConnection",
        description="List available clusters for subscription (paginated).",
    )
    "List available clusters for subscription (paginated)."


class GetClustersAvailableConnectionClustersAvailableConnection(BaseModel):
    edges: list["GetClustersAvailableConnectionClustersAvailableConnectionEdges"]
    page_info: "GetClustersAvailableConnectionClustersAvailableConnectionPageInfo" = (
        Field(alias="pageInfo")
    )
    total_count: int = Field(alias="totalCount")


class GetClustersAvailableConnectionClustersAvailableConnectionEdges(BaseModel):
    cursor: str
    node: "GetClustersAvailableConnectionClustersAvailableConnectionEdgesNode"


GetClustersAvailableConnectionClustersAvailableConnectionEdgesNode = (
    AvailableClusterDefault
)
GetClustersAvailableConnectionClustersAvailableConnectionPageInfo = PageInfoDefault
GetClustersAvailableConnection.model_rebuild()
GetClustersAvailableConnectionClustersAvailableConnection.model_rebuild()
GetClustersAvailableConnectionClustersAvailableConnectionEdges.model_rebuild()
