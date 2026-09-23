from pydantic import Field

from .base_model import BaseModel
from .fragments import MessageDefault, PageInfoDefault


class GetMessagesConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    messages_connection: "GetMessagesConnectionMessagesConnection" = Field(
        alias="messagesConnection",
        description="List messages in a conversation.\nMessages are ordered chronologically, oldest first.",
    )
    "List messages in a conversation.\nMessages are ordered chronologically, oldest first."


class GetMessagesConnectionMessagesConnection(BaseModel):
    """Paginated list of messages."""

    edges: list["GetMessagesConnectionMessagesConnectionEdges"] = Field(
        description="List of messages."
    )
    "List of messages."
    page_info: "GetMessagesConnectionMessagesConnectionPageInfo" = Field(
        alias="pageInfo", description="Pagination information."
    )
    "Pagination information."
    total_count: int = Field(alias="totalCount", description="Total count of messages.")
    "Total count of messages."


class GetMessagesConnectionMessagesConnectionEdges(BaseModel):
    """A message edge in a connection."""

    cursor: str = Field(description="Cursor for this edge.")
    "Cursor for this edge."
    node: "GetMessagesConnectionMessagesConnectionEdgesNode" = Field(
        description="The message."
    )
    "The message."


class GetMessagesConnectionMessagesConnectionEdgesNode(MessageDefault):
    """A message within a support conversation."""

    pass


GetMessagesConnectionMessagesConnectionPageInfo = PageInfoDefault
GetMessagesConnection.model_rebuild()
GetMessagesConnectionMessagesConnection.model_rebuild()
GetMessagesConnectionMessagesConnectionEdges.model_rebuild()
