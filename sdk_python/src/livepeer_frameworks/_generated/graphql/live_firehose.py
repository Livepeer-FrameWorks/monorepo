from pydantic import Field

from .base_model import BaseModel
from .fragments import TenantEventDefault


class LiveFirehose(BaseModel):
    """Real-time subscriptions for live event streaming via WebSocket.
    All subscriptions are tenant-scoped and require authentication.
    Events are delivered as they occur with minimal latency."""

    live_firehose: "LiveFirehoseLiveFirehose" = Field(
        alias="liveFirehose",
        description="Firehose subscription receiving ALL tenant events in a single stream.\nCombines stream, analytics, and system events for unified dashboards.\nUse the type and channel fields to filter/route events client-side.",
    )
    "Firehose subscription receiving ALL tenant events in a single stream.\nCombines stream, analytics, and system events for unified dashboards.\nUse the type and channel fields to filter/route events client-side."


LiveFirehoseLiveFirehose = TenantEventDefault
LiveFirehose.model_rebuild()
