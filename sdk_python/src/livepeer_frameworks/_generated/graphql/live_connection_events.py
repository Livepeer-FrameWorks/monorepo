from pydantic import Field

from .base_model import BaseModel
from .fragments import ConnectionEventDefault


class LiveConnectionEvents(BaseModel):
    """Real-time subscriptions for live event streaming via WebSocket.
    All subscriptions are tenant-scoped and require authentication.
    Events are delivered as they occur with minimal latency."""

    live_connection_events: "LiveConnectionEventsLiveConnectionEvents" = Field(
        alias="liveConnectionEvents",
        description="Individual viewer connection/disconnection events.\nHigh volume - filter by streamId in production for performance.",
    )
    "Individual viewer connection/disconnection events.\nHigh volume - filter by streamId in production for performance."


LiveConnectionEventsLiveConnectionEvents = ConnectionEventDefault
LiveConnectionEvents.model_rebuild()
