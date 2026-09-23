from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import AuthErrorDefault, NotFoundErrorDefault, WebhookEndpointDefault


class EnableWebhookEndpoint(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    enable_webhook_endpoint: Annotated[
        Union[
            "EnableWebhookEndpointEnableWebhookEndpointWebhookEndpoint",
            "EnableWebhookEndpointEnableWebhookEndpointNotFoundError",
            "EnableWebhookEndpointEnableWebhookEndpointAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="enableWebhookEndpoint",
        description="Enable a disabled endpoint. Deliveries skipped while it was disabled are not\nsent again; replay them with replayWebhookDeliveries.",
    )
    "Enable a disabled endpoint. Deliveries skipped while it was disabled are not\nsent again; replay them with replayWebhookDeliveries."


class EnableWebhookEndpointEnableWebhookEndpointWebhookEndpoint(WebhookEndpointDefault):
    typename__: Literal["WebhookEndpoint"] = Field(alias="__typename")


class EnableWebhookEndpointEnableWebhookEndpointNotFoundError(NotFoundErrorDefault):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class EnableWebhookEndpointEnableWebhookEndpointAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


EnableWebhookEndpoint.model_rebuild()
