from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (
    AuthErrorDefault,
    NotFoundErrorDefault,
    ValidationErrorDefault,
    WebhookEndpointDefault,
)


class UpdateWebhookEndpoint(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    update_webhook_endpoint: Annotated[
        Union[
            "UpdateWebhookEndpointUpdateWebhookEndpointWebhookEndpoint",
            "UpdateWebhookEndpointUpdateWebhookEndpointValidationError",
            "UpdateWebhookEndpointUpdateWebhookEndpointNotFoundError",
            "UpdateWebhookEndpointUpdateWebhookEndpointAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="updateWebhookEndpoint",
        description="Change an endpoint's URL, description, or event types. Omitted fields keep\ntheir value.",
    )
    "Change an endpoint's URL, description, or event types. Omitted fields keep\ntheir value."


class UpdateWebhookEndpointUpdateWebhookEndpointWebhookEndpoint(WebhookEndpointDefault):
    typename__: Literal["WebhookEndpoint"] = Field(alias="__typename")


class UpdateWebhookEndpointUpdateWebhookEndpointValidationError(ValidationErrorDefault):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class UpdateWebhookEndpointUpdateWebhookEndpointNotFoundError(NotFoundErrorDefault):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class UpdateWebhookEndpointUpdateWebhookEndpointAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


UpdateWebhookEndpoint.model_rebuild()
