from pydantic import Field

from .base_model import BaseModel
from .fragments import StreamEventDefault


class LiveStreamEvents(BaseModel):
    """Real-time subscriptions for live event streaming via WebSocket.
    All subscriptions are tenant-scoped and require authentication.
    Events are delivered as they occur with minimal latency."""

    live_stream_events: "LiveStreamEventsLiveStreamEvents" = Field(
        alias="liveStreamEvents",
        description="Stream lifecycle events including start, stop, and health changes.\nOptionally filter to a specific stream, or receive all tenant streams.",
    )
    "Stream lifecycle events including start, stop, and health changes.\nOptionally filter to a specific stream, or receive all tenant streams."


LiveStreamEventsLiveStreamEvents = StreamEventDefault
LiveStreamEvents.model_rebuild()
