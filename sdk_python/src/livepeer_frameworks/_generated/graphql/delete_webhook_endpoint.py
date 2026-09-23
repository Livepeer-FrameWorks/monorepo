from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import AuthErrorDefault, DeleteSuccessDefault, NotFoundErrorDefault


class DeleteWebhookEndpoint(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    delete_webhook_endpoint: Annotated[
        Union[
            "DeleteWebhookEndpointDeleteWebhookEndpointDeleteSuccess",
            "DeleteWebhookEndpointDeleteWebhookEndpointNotFoundError",
            "DeleteWebhookEndpointDeleteWebhookEndpointAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="deleteWebhookEndpoint",
        description="Delete an endpoint with its signing secrets and delivery log.",
    )
    "Delete an endpoint with its signing secrets and delivery log."


class DeleteWebhookEndpointDeleteWebhookEndpointDeleteSuccess(DeleteSuccessDefault):
    typename__: Literal["DeleteSuccess"] = Field(alias="__typename")


class DeleteWebhookEndpointDeleteWebhookEndpointNotFoundError(NotFoundErrorDefault):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class DeleteWebhookEndpointDeleteWebhookEndpointAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


DeleteWebhookEndpoint.model_rebuild()
