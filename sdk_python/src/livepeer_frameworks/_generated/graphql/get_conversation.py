from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import (
    ConversationDefault,
    ConversationDefaultLastMessage,  # noqa: F401
)


class GetConversation(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    conversation: Optional["GetConversationConversation"] = Field(
        description="Fetch a single conversation by ID."
    )
    "Fetch a single conversation by ID."


class GetConversationConversation(ConversationDefault):
    """A support conversation between tenant and support team.
    Conversations can contain multiple messages and have a status."""

    pass


GetConversation.model_rebuild()
