from pydantic import Field

from .base_model import BaseModel
from .fragments import (  # noqa: F401
    InfrastructureNodeDefault,
    InfrastructureNodeDefaultLiveState,
    InfrastructureNodeDefaultRoutingImpactPreview,
    PageInfoDefault,
)


class GetNodesConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    nodes_connection: "GetNodesConnectionNodesConnection" = Field(
        alias="nodesConnection",
        description="List nodes (edge servers) with optional filters.",
    )
    "List nodes (edge servers) with optional filters."


class GetNodesConnectionNodesConnection(BaseModel):
    edges: list["GetNodesConnectionNodesConnectionEdges"]
    page_info: "GetNodesConnectionNodesConnectionPageInfo" = Field(alias="pageInfo")
    total_count: int = Field(alias="totalCount")


class GetNodesConnectionNodesConnectionEdges(BaseModel):
    cursor: str
    node: "GetNodesConnectionNodesConnectionEdgesNode"


class GetNodesConnectionNodesConnectionEdgesNode(InfrastructureNodeDefault):
    """An infrastructure node in a cluster (edge server, origin, transcoder).
    Nodes handle stream ingest, transcoding, and delivery to viewers."""

    pass


GetNodesConnectionNodesConnectionPageInfo = PageInfoDefault
GetNodesConnection.model_rebuild()
GetNodesConnectionNodesConnection.model_rebuild()
GetNodesConnectionNodesConnectionEdges.model_rebuild()
