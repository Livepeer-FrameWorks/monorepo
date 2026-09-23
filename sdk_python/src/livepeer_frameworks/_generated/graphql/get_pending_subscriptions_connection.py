from pydantic import Field

from .base_model import BaseModel
from .fragments import ClusterSubscriptionDefault, PageInfoDefault


class GetPendingSubscriptionsConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    pending_subscriptions_connection: "GetPendingSubscriptionsConnectionPendingSubscriptionsConnection" = Field(
        alias="pendingSubscriptionsConnection",
        description="List pending subscription requests (paginated).",
    )
    "List pending subscription requests (paginated)."


class GetPendingSubscriptionsConnectionPendingSubscriptionsConnection(BaseModel):
    edges: list["GetPendingSubscriptionsConnectionPendingSubscriptionsConnectionEdges"]
    page_info: "GetPendingSubscriptionsConnectionPendingSubscriptionsConnectionPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


class GetPendingSubscriptionsConnectionPendingSubscriptionsConnectionEdges(BaseModel):
    cursor: str
    node: "GetPendingSubscriptionsConnectionPendingSubscriptionsConnectionEdgesNode"


GetPendingSubscriptionsConnectionPendingSubscriptionsConnectionEdgesNode = (
    ClusterSubscriptionDefault
)
GetPendingSubscriptionsConnectionPendingSubscriptionsConnectionPageInfo = (
    PageInfoDefault
)
GetPendingSubscriptionsConnection.model_rebuild()
GetPendingSubscriptionsConnectionPendingSubscriptionsConnection.model_rebuild()
GetPendingSubscriptionsConnectionPendingSubscriptionsConnectionEdges.model_rebuild()
