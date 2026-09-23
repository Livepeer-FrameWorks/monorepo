from pydantic import Field

from .base_model import BaseModel
from .fragments import (
    ConversationDefault,
    ConversationDefaultLastMessage,  # noqa: F401
    PageInfoDefault,
)


class GetConversationsConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    conversations_connection: "GetConversationsConnectionConversationsConnection" = Field(
        alias="conversationsConnection",
        description="List all support conversations for the current tenant.\nConversations are ordered by last activity, most recent first.",
    )
    "List all support conversations for the current tenant.\nConversations are ordered by last activity, most recent first."


class GetConversationsConnectionConversationsConnection(BaseModel):
    """Paginated list of conversations."""

    edges: list["GetConversationsConnectionConversationsConnectionEdges"] = Field(
        description="List of conversations."
    )
    "List of conversations."
    page_info: "GetConversationsConnectionConversationsConnectionPageInfo" = Field(
        alias="pageInfo", description="Pagination information."
    )
    "Pagination information."
    total_count: int = Field(
        alias="totalCount", description="Total count of conversations."
    )
    "Total count of conversations."


class GetConversationsConnectionConversationsConnectionEdges(BaseModel):
    """A conversation edge in a connection."""

    cursor: str = Field(description="Cursor for this edge.")
    "Cursor for this edge."
    node: "GetConversationsConnectionConversationsConnectionEdgesNode" = Field(
        description="The conversation."
    )
    "The conversation."


class GetConversationsConnectionConversationsConnectionEdgesNode(ConversationDefault):
    """A support conversation between tenant and support team.
    Conversations can contain multiple messages and have a status."""

    pass


GetConversationsConnectionConversationsConnectionPageInfo = PageInfoDefault
GetConversationsConnection.model_rebuild()
GetConversationsConnectionConversationsConnection.model_rebuild()
GetConversationsConnectionConversationsConnectionEdges.model_rebuild()
