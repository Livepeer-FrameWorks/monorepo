from pydantic import Field

from .base_model import BaseModel
from .fragments import MarketplaceClusterDefault, PageInfoDefault


class GetMarketplaceClustersConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    marketplace_clusters_connection: "GetMarketplaceClustersConnectionMarketplaceClustersConnection" = Field(
        alias="marketplaceClustersConnection",
        description="List clusters available in the marketplace (paginated).",
    )
    "List clusters available in the marketplace (paginated)."


class GetMarketplaceClustersConnectionMarketplaceClustersConnection(BaseModel):
    edges: list["GetMarketplaceClustersConnectionMarketplaceClustersConnectionEdges"]
    page_info: "GetMarketplaceClustersConnectionMarketplaceClustersConnectionPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


class GetMarketplaceClustersConnectionMarketplaceClustersConnectionEdges(BaseModel):
    cursor: str
    node: "GetMarketplaceClustersConnectionMarketplaceClustersConnectionEdgesNode"


GetMarketplaceClustersConnectionMarketplaceClustersConnectionEdgesNode = (
    MarketplaceClusterDefault
)
GetMarketplaceClustersConnectionMarketplaceClustersConnectionPageInfo = PageInfoDefault
GetMarketplaceClustersConnection.model_rebuild()
GetMarketplaceClustersConnectionMarketplaceClustersConnection.model_rebuild()
GetMarketplaceClustersConnectionMarketplaceClustersConnectionEdges.model_rebuild()
