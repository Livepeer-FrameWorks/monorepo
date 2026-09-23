from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import MarketplaceClusterDefault


class GetMarketplaceCluster(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    marketplace_cluster: Optional["GetMarketplaceClusterMarketplaceCluster"] = Field(
        alias="marketplaceCluster",
        description="Get details for a specific marketplace cluster.",
    )
    "Get details for a specific marketplace cluster."


GetMarketplaceClusterMarketplaceCluster = MarketplaceClusterDefault
GetMarketplaceCluster.model_rebuild()
