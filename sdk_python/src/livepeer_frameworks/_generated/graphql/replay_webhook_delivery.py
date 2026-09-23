from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (
    AuthErrorDefault,
    NotFoundErrorDefault,
    ValidationErrorDefault,
    WebhookDeliveryDefault,
    WebhookDeliveryDefaultAttemptHistory,  # noqa: F401
)


class ReplayWebhookDelivery(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    replay_webhook_delivery: Annotated[
        Union[
            "ReplayWebhookDeliveryReplayWebhookDeliveryWebhookDelivery",
            "ReplayWebhookDeliveryReplayWebhookDeliveryValidationError",
            "ReplayWebhookDeliveryReplayWebhookDeliveryNotFoundError",
            "ReplayWebhookDeliveryReplayWebhookDeliveryAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="replayWebhookDelivery",
        description="Send a finished delivery again under the same ID, so the receiver sees the\nsame webhook-id. The endpoint must be enabled.",
    )
    "Send a finished delivery again under the same ID, so the receiver sees the\nsame webhook-id. The endpoint must be enabled."


class ReplayWebhookDeliveryReplayWebhookDeliveryWebhookDelivery(WebhookDeliveryDefault):
    typename__: Literal["WebhookDelivery"] = Field(alias="__typename")


class ReplayWebhookDeliveryReplayWebhookDeliveryValidationError(ValidationErrorDefault):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class ReplayWebhookDeliveryReplayWebhookDeliveryNotFoundError(NotFoundErrorDefault):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class ReplayWebhookDeliveryReplayWebhookDeliveryAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


ReplayWebhookDelivery.model_rebuild()
