from pydantic import Field

from .base_model import BaseModel
from .fragments import ClusterInviteDefault, PageInfoDefault


class GetMyClusterInvitesConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    my_cluster_invites_connection: "GetMyClusterInvitesConnectionMyClusterInvitesConnection" = Field(
        alias="myClusterInvitesConnection",
        description="List cluster invites sent to the current tenant (paginated).",
    )
    "List cluster invites sent to the current tenant (paginated)."


class GetMyClusterInvitesConnectionMyClusterInvitesConnection(BaseModel):
    edges: list["GetMyClusterInvitesConnectionMyClusterInvitesConnectionEdges"]
    page_info: "GetMyClusterInvitesConnectionMyClusterInvitesConnectionPageInfo" = (
        Field(alias="pageInfo")
    )
    total_count: int = Field(alias="totalCount")


class GetMyClusterInvitesConnectionMyClusterInvitesConnectionEdges(BaseModel):
    cursor: str
    node: "GetMyClusterInvitesConnectionMyClusterInvitesConnectionEdgesNode"


GetMyClusterInvitesConnectionMyClusterInvitesConnectionEdgesNode = ClusterInviteDefault
GetMyClusterInvitesConnectionMyClusterInvitesConnectionPageInfo = PageInfoDefault
GetMyClusterInvitesConnection.model_rebuild()
GetMyClusterInvitesConnectionMyClusterInvitesConnection.model_rebuild()
GetMyClusterInvitesConnectionMyClusterInvitesConnectionEdges.model_rebuild()
