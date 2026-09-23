from pydantic import Field

from .base_model import BaseModel
from .fragments import TrackListUpdateDefault


class LiveTrackListUpdates(BaseModel):
    """Real-time subscriptions for live event streaming via WebSocket.
    All subscriptions are tenant-scoped and require authentication.
    Events are delivered as they occur with minimal latency."""

    live_track_list_updates: "LiveTrackListUpdatesLiveTrackListUpdates" = Field(
        alias="liveTrackListUpdates",
        description="Track list updates when stream quality tiers change.\nFires when tracks are added, removed, or quality changes.",
    )
    "Track list updates when stream quality tiers change.\nFires when tracks are added, removed, or quality changes."


LiveTrackListUpdatesLiveTrackListUpdates = TrackListUpdateDefault
LiveTrackListUpdates.model_rebuild()
