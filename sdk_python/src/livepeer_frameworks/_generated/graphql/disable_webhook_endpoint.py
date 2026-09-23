from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import AuthErrorDefault, NotFoundErrorDefault, WebhookEndpointDefault


class DisableWebhookEndpoint(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    disable_webhook_endpoint: Annotated[
        Union[
            "DisableWebhookEndpointDisableWebhookEndpointWebhookEndpoint",
            "DisableWebhookEndpointDisableWebhookEndpointNotFoundError",
            "DisableWebhookEndpointDisableWebhookEndpointAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="disableWebhookEndpoint",
        description="Disable an endpoint. Its pending deliveries are skipped.",
    )
    "Disable an endpoint. Its pending deliveries are skipped."


class DisableWebhookEndpointDisableWebhookEndpointWebhookEndpoint(
    WebhookEndpointDefault
):
    typename__: Literal["WebhookEndpoint"] = Field(alias="__typename")


class DisableWebhookEndpointDisableWebhookEndpointNotFoundError(NotFoundErrorDefault):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class DisableWebhookEndpointDisableWebhookEndpointAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


DisableWebhookEndpoint.model_rebuild()
