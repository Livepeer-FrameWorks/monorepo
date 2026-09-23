from pydantic import Field

from .base_model import BaseModel


class GetWebhookEventTypes(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    webhook_event_types: list[str] = Field(
        alias="webhookEventTypes",
        description='The public event types an endpoint can subscribe to, as used in\neventTypes. "*" subscribes to every type.',
    )
    'The public event types an endpoint can subscribe to, as used in\neventTypes. "*" subscribes to every type.'
