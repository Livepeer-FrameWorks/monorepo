from pydantic import Field

from .base_model import BaseModel
from .fragments import (
    ConversationDefault,
    ConversationDefaultLastMessage,  # noqa: F401
)


class LiveConversationUpdates(BaseModel):
    """Real-time subscriptions for live event streaming via WebSocket.
    All subscriptions are tenant-scoped and require authentication.
    Events are delivered as they occur with minimal latency."""

    live_conversation_updates: "LiveConversationUpdatesLiveConversationUpdates" = Field(
        alias="liveConversationUpdates",
        description="Real-time conversation lifecycle updates (created/updated/status changes).\nOptionally filter by conversationId.",
    )
    "Real-time conversation lifecycle updates (created/updated/status changes).\nOptionally filter by conversationId."


class LiveConversationUpdatesLiveConversationUpdates(ConversationDefault):
    """A support conversation between tenant and support team.
    Conversations can contain multiple messages and have a status."""

    pass


LiveConversationUpdates.model_rebuild()
