from pydantic import Field

from .base_model import BaseModel
from .fragments import SkipperConversationSummaryDefault


class GetSkipperConversations(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    skipper_conversations: list["GetSkipperConversationsSkipperConversations"] = Field(
        alias="skipperConversations",
        description="List Skipper AI consultant conversations for the current user.",
    )
    "List Skipper AI consultant conversations for the current user."


GetSkipperConversationsSkipperConversations = SkipperConversationSummaryDefault
GetSkipperConversations.model_rebuild()
