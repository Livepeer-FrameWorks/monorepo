from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import (
    WebhookDeliveryDefault,
    WebhookDeliveryDefaultAttemptHistory,  # noqa: F401
)


class GetWebhookDelivery(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    webhook_delivery: Optional["GetWebhookDeliveryWebhookDelivery"] = Field(
        alias="webhookDelivery",
        description="Get one webhook delivery with every HTTP attempt.",
    )
    "Get one webhook delivery with every HTTP attempt."


class GetWebhookDeliveryWebhookDelivery(WebhookDeliveryDefault):
    """One delivery of one event to one endpoint, or a test delivery."""

    pass


GetWebhookDelivery.model_rebuild()
