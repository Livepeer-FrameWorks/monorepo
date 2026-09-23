from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (
    AuthErrorDefault,
    NotFoundErrorDefault,
    WebhookEndpointSecretDefault,
    WebhookEndpointSecretDefaultEndpoint,  # noqa: F401
)


class RotateWebhookEndpointSecret(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    rotate_webhook_endpoint_secret: Annotated[
        Union[
            "RotateWebhookEndpointSecretRotateWebhookEndpointSecretWebhookEndpointSecret",
            "RotateWebhookEndpointSecretRotateWebhookEndpointSecretNotFoundError",
            "RotateWebhookEndpointSecretRotateWebhookEndpointSecretAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="rotateWebhookEndpointSecret",
        description="Replace an endpoint's signing secret and return the new one once. The\nprevious secret keeps signing alongside it for 24 hours, unless\nrevokePrevious is true.",
    )
    "Replace an endpoint's signing secret and return the new one once. The\nprevious secret keeps signing alongside it for 24 hours, unless\nrevokePrevious is true."


class RotateWebhookEndpointSecretRotateWebhookEndpointSecretWebhookEndpointSecret(
    WebhookEndpointSecretDefault
):
    typename__: Literal["WebhookEndpointSecret"] = Field(alias="__typename")


class RotateWebhookEndpointSecretRotateWebhookEndpointSecretNotFoundError(
    NotFoundErrorDefault
):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class RotateWebhookEndpointSecretRotateWebhookEndpointSecretAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


RotateWebhookEndpointSecret.model_rebuild()
