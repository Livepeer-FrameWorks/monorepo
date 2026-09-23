from pydantic import Field

from .base_model import BaseModel
from .fragments import ViewerMetricsDefault


class LiveViewerMetrics(BaseModel):
    """Real-time subscriptions for live event streaming via WebSocket.
    All subscriptions are tenant-scoped and require authentication.
    Events are delivered as they occur with minimal latency."""

    live_viewer_metrics: "LiveViewerMetricsLiveViewerMetrics" = Field(
        alias="liveViewerMetrics",
        description="Real-time viewer count and engagement metrics for a stream.\nUpdates every few seconds while the stream is live.",
    )
    "Real-time viewer count and engagement metrics for a stream.\nUpdates every few seconds while the stream is live."


LiveViewerMetricsLiveViewerMetrics = ViewerMetricsDefault
LiveViewerMetrics.model_rebuild()
