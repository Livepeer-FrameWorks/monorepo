from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (
    AuthErrorDefault,
    ValidationErrorDefault,
    WebhookEndpointSecretDefault,
    WebhookEndpointSecretDefaultEndpoint,  # noqa: F401
)


class CreateWebhookEndpoint(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    create_webhook_endpoint: Annotated[
        Union[
            "CreateWebhookEndpointCreateWebhookEndpointWebhookEndpointSecret",
            "CreateWebhookEndpointCreateWebhookEndpointValidationError",
            "CreateWebhookEndpointCreateWebhookEndpointAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="createWebhookEndpoint",
        description="Create an outbound webhook endpoint. The response carries the signing secret\nonce; it is never returned again. A tenant can have at most 10 endpoints.",
    )
    "Create an outbound webhook endpoint. The response carries the signing secret\nonce; it is never returned again. A tenant can have at most 10 endpoints."


class CreateWebhookEndpointCreateWebhookEndpointWebhookEndpointSecret(
    WebhookEndpointSecretDefault
):
    typename__: Literal["WebhookEndpointSecret"] = Field(alias="__typename")


class CreateWebhookEndpointCreateWebhookEndpointValidationError(ValidationErrorDefault):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class CreateWebhookEndpointCreateWebhookEndpointAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


CreateWebhookEndpoint.model_rebuild()
