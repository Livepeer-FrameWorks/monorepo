from pydantic import Field

from .base_model import BaseModel
from .fragments import MessageDefault


class LiveMessageReceived(BaseModel):
    """Real-time subscriptions for live event streaming via WebSocket.
    All subscriptions are tenant-scoped and require authentication.
    Events are delivered as they occur with minimal latency."""

    live_message_received: "LiveMessageReceivedLiveMessageReceived" = Field(
        alias="liveMessageReceived",
        description="Real-time message events for a conversation.\nFires when agent replies are received.",
    )
    "Real-time message events for a conversation.\nFires when agent replies are received."


class LiveMessageReceivedLiveMessageReceived(MessageDefault):
    """A message within a support conversation."""

    pass


LiveMessageReceived.model_rebuild()
