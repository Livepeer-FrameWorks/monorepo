from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (
    AuthErrorDefault,
    NotFoundErrorDefault,
    ValidationErrorDefault,
    WebhookReplayResultDefault,
)


class ReplayWebhookDeliveries(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    replay_webhook_deliveries: Annotated[
        Union[
            "ReplayWebhookDeliveriesReplayWebhookDeliveriesWebhookReplayResult",
            "ReplayWebhookDeliveriesReplayWebhookDeliveriesValidationError",
            "ReplayWebhookDeliveriesReplayWebhookDeliveriesNotFoundError",
            "ReplayWebhookDeliveriesReplayWebhookDeliveriesAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="replayWebhookDeliveries",
        description="Send again the failed and skipped deliveries of one endpoint created in\n[createdAfter, createdBefore), oldest first, at most 1000 per call. Call\nagain while hasMore is true.",
    )
    "Send again the failed and skipped deliveries of one endpoint created in\n[createdAfter, createdBefore), oldest first, at most 1000 per call. Call\nagain while hasMore is true."


class ReplayWebhookDeliveriesReplayWebhookDeliveriesWebhookReplayResult(
    WebhookReplayResultDefault
):
    typename__: Literal["WebhookReplayResult"] = Field(alias="__typename")


class ReplayWebhookDeliveriesReplayWebhookDeliveriesValidationError(
    ValidationErrorDefault
):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class ReplayWebhookDeliveriesReplayWebhookDeliveriesNotFoundError(NotFoundErrorDefault):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class ReplayWebhookDeliveriesReplayWebhookDeliveriesAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


ReplayWebhookDeliveries.model_rebuild()
