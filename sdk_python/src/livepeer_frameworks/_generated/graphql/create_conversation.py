from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (
    AuthErrorDefault,
    ConversationDefault,
    ConversationDefaultLastMessage,  # noqa: F401
    ValidationErrorDefault,
)


class CreateConversation(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    create_conversation: Annotated[
        Union[
            "CreateConversationCreateConversationConversation",
            "CreateConversationCreateConversationValidationError",
            "CreateConversationCreateConversationAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="createConversation",
        description="Create a new support conversation.\nOptionally include an initial message.",
    )
    "Create a new support conversation.\nOptionally include an initial message."


class CreateConversationCreateConversationConversation(ConversationDefault):
    typename__: Literal["Conversation"] = Field(alias="__typename")


class CreateConversationCreateConversationValidationError(ValidationErrorDefault):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class CreateConversationCreateConversationAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


CreateConversation.model_rebuild()
