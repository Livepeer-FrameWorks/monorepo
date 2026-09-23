from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import WebhookEndpointDefault


class GetWebhookEndpoint(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    webhook_endpoint: Optional["GetWebhookEndpointWebhookEndpoint"] = Field(
        alias="webhookEndpoint",
        description="Get one of the tenant's webhook endpoints.",
    )
    "Get one of the tenant's webhook endpoints."


class GetWebhookEndpointWebhookEndpoint(WebhookEndpointDefault):
    """An outbound webhook endpoint. FrameWorks POSTs the tenant's public events of
    the subscribed types to its URL, signed with the Standard Webhooks scheme
    (webhook-id, webhook-timestamp, webhook-signature headers)."""

    pass


GetWebhookEndpoint.model_rebuild()
