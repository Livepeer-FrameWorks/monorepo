from pydantic import Field

from .base_model import BaseModel
from .fragments import ClusterDefault, PageInfoDefault


class GetMySubscriptionsConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    my_subscriptions_connection: "GetMySubscriptionsConnectionMySubscriptionsConnection" = Field(
        alias="mySubscriptionsConnection",
        description="List clusters the tenant is subscribed to (paginated).",
    )
    "List clusters the tenant is subscribed to (paginated)."


class GetMySubscriptionsConnectionMySubscriptionsConnection(BaseModel):
    edges: list["GetMySubscriptionsConnectionMySubscriptionsConnectionEdges"]
    page_info: "GetMySubscriptionsConnectionMySubscriptionsConnectionPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


class GetMySubscriptionsConnectionMySubscriptionsConnectionEdges(BaseModel):
    cursor: str
    node: "GetMySubscriptionsConnectionMySubscriptionsConnectionEdgesNode"


class GetMySubscriptionsConnectionMySubscriptionsConnectionEdgesNode(ClusterDefault):
    """A streaming cluster containing one or more infrastructure nodes.
    Clusters provide isolated capacity for streaming workloads."""

    pass


GetMySubscriptionsConnectionMySubscriptionsConnectionPageInfo = PageInfoDefault
GetMySubscriptionsConnection.model_rebuild()
GetMySubscriptionsConnectionMySubscriptionsConnection.model_rebuild()
GetMySubscriptionsConnectionMySubscriptionsConnectionEdges.model_rebuild()
