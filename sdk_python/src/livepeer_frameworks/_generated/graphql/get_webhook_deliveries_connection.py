from pydantic import Field

from .base_model import BaseModel
from .fragments import (
    PageInfoDefault,
    WebhookDeliveryDefault,
    WebhookDeliveryDefaultAttemptHistory,  # noqa: F401
)


class GetWebhookDeliveriesConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    webhook_deliveries_connection: "GetWebhookDeliveriesConnectionWebhookDeliveriesConnection" = Field(
        alias="webhookDeliveriesConnection",
        description="List webhook deliveries newest first, optionally filtered. Only forward\npagination is supported.",
    )
    "List webhook deliveries newest first, optionally filtered. Only forward\npagination is supported."


class GetWebhookDeliveriesConnectionWebhookDeliveriesConnection(BaseModel):
    edges: list["GetWebhookDeliveriesConnectionWebhookDeliveriesConnectionEdges"]
    page_info: "GetWebhookDeliveriesConnectionWebhookDeliveriesConnectionPageInfo" = (
        Field(alias="pageInfo")
    )
    total_count: int = Field(alias="totalCount")


class GetWebhookDeliveriesConnectionWebhookDeliveriesConnectionEdges(BaseModel):
    cursor: str
    node: "GetWebhookDeliveriesConnectionWebhookDeliveriesConnectionEdgesNode"


class GetWebhookDeliveriesConnectionWebhookDeliveriesConnectionEdgesNode(
    WebhookDeliveryDefault
):
    """One delivery of one event to one endpoint, or a test delivery."""

    pass


GetWebhookDeliveriesConnectionWebhookDeliveriesConnectionPageInfo = PageInfoDefault
GetWebhookDeliveriesConnection.model_rebuild()
GetWebhookDeliveriesConnectionWebhookDeliveriesConnection.model_rebuild()
GetWebhookDeliveriesConnectionWebhookDeliveriesConnectionEdges.model_rebuild()
