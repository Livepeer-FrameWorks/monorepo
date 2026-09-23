from pydantic import Field

from .base_model import BaseModel
from .fragments import ClusterAccessDefault, PageInfoDefault


class GetClustersAccessConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    clusters_access_connection: "GetClustersAccessConnectionClustersAccessConnection" = Field(
        alias="clustersAccessConnection",
        description="List clusters the tenant has access to (paginated).",
    )
    "List clusters the tenant has access to (paginated)."


class GetClustersAccessConnectionClustersAccessConnection(BaseModel):
    edges: list["GetClustersAccessConnectionClustersAccessConnectionEdges"]
    page_info: "GetClustersAccessConnectionClustersAccessConnectionPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


class GetClustersAccessConnectionClustersAccessConnectionEdges(BaseModel):
    cursor: str
    node: "GetClustersAccessConnectionClustersAccessConnectionEdgesNode"


GetClustersAccessConnectionClustersAccessConnectionEdgesNode = ClusterAccessDefault
GetClustersAccessConnectionClustersAccessConnectionPageInfo = PageInfoDefault
GetClustersAccessConnection.model_rebuild()
GetClustersAccessConnectionClustersAccessConnection.model_rebuild()
GetClustersAccessConnectionClustersAccessConnectionEdges.model_rebuild()
