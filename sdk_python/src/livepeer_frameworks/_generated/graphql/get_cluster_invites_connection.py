from pydantic import Field

from .base_model import BaseModel
from .fragments import ClusterInviteDefault, PageInfoDefault


class GetClusterInvitesConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    cluster_invites_connection: "GetClusterInvitesConnectionClusterInvitesConnection" = Field(
        alias="clusterInvitesConnection",
        description="List invites for a cluster (paginated).",
    )
    "List invites for a cluster (paginated)."


class GetClusterInvitesConnectionClusterInvitesConnection(BaseModel):
    edges: list["GetClusterInvitesConnectionClusterInvitesConnectionEdges"]
    page_info: "GetClusterInvitesConnectionClusterInvitesConnectionPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


class GetClusterInvitesConnectionClusterInvitesConnectionEdges(BaseModel):
    cursor: str
    node: "GetClusterInvitesConnectionClusterInvitesConnectionEdgesNode"


GetClusterInvitesConnectionClusterInvitesConnectionEdgesNode = ClusterInviteDefault
GetClusterInvitesConnectionClusterInvitesConnectionPageInfo = PageInfoDefault
GetClusterInvitesConnection.model_rebuild()
GetClusterInvitesConnectionClusterInvitesConnection.model_rebuild()
GetClusterInvitesConnectionClusterInvitesConnectionEdges.model_rebuild()
