from pydantic import Field

from .base_model import BaseModel
from .fragments import SkipperConversationDefault


class GetSkipperConversation(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    skipper_conversation: "GetSkipperConversationSkipperConversation" = Field(
        alias="skipperConversation",
        description="Get a single Skipper conversation with full message history.",
    )
    "Get a single Skipper conversation with full message history."


GetSkipperConversationSkipperConversation = SkipperConversationDefault
GetSkipperConversation.model_rebuild()
