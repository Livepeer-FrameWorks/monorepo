from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (
    AuthErrorDefault,
    MessageDefault,
    NotFoundErrorDefault,
    ValidationErrorDefault,
)


class SendMessage(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    send_message: Annotated[
        Union[
            "SendMessageSendMessageMessage",
            "SendMessageSendMessageValidationError",
            "SendMessageSendMessageNotFoundError",
            "SendMessageSendMessageAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="sendMessage",
        description="Send a message in an existing conversation.\nMessages are delivered to support agents in real-time.",
    )
    "Send a message in an existing conversation.\nMessages are delivered to support agents in real-time."


class SendMessageSendMessageMessage(MessageDefault):
    typename__: Literal["Message"] = Field(alias="__typename")


class SendMessageSendMessageValidationError(ValidationErrorDefault):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class SendMessageSendMessageNotFoundError(NotFoundErrorDefault):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class SendMessageSendMessageAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


SendMessage.model_rebuild()
