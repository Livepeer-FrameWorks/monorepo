from pydantic import Field

from .base_model import BaseModel
from .fragments import PageInfoDefault, WebhookEndpointDefault


class GetWebhookEndpointsConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    webhook_endpoints_connection: "GetWebhookEndpointsConnectionWebhookEndpointsConnection" = Field(
        alias="webhookEndpointsConnection",
        description="List the tenant's webhook endpoints, oldest first. A tenant has at most 10,\nso one page holds them all.",
    )
    "List the tenant's webhook endpoints, oldest first. A tenant has at most 10,\nso one page holds them all."


class GetWebhookEndpointsConnectionWebhookEndpointsConnection(BaseModel):
    edges: list["GetWebhookEndpointsConnectionWebhookEndpointsConnectionEdges"]
    page_info: "GetWebhookEndpointsConnectionWebhookEndpointsConnectionPageInfo" = (
        Field(alias="pageInfo")
    )
    total_count: int = Field(alias="totalCount")


class GetWebhookEndpointsConnectionWebhookEndpointsConnectionEdges(BaseModel):
    cursor: str
    node: "GetWebhookEndpointsConnectionWebhookEndpointsConnectionEdgesNode"


class GetWebhookEndpointsConnectionWebhookEndpointsConnectionEdgesNode(
    WebhookEndpointDefault
):
    """An outbound webhook endpoint. FrameWorks POSTs the tenant's public events of
    the subscribed types to its URL, signed with the Standard Webhooks scheme
    (webhook-id, webhook-timestamp, webhook-signature headers)."""

    pass


GetWebhookEndpointsConnectionWebhookEndpointsConnectionPageInfo = PageInfoDefault
GetWebhookEndpointsConnection.model_rebuild()
GetWebhookEndpointsConnectionWebhookEndpointsConnection.model_rebuild()
GetWebhookEndpointsConnectionWebhookEndpointsConnectionEdges.model_rebuild()
