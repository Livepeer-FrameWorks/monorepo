from pydantic import Field

from .base_model import BaseModel
from .fragments import SystemHealthEventDefault


class LiveSystemHealth(BaseModel):
    """Real-time subscriptions for live event streaming via WebSocket.
    All subscriptions are tenant-scoped and require authentication.
    Events are delivered as they occur with minimal latency."""

    live_system_health: "LiveSystemHealthLiveSystemHealth" = Field(
        alias="liveSystemHealth",
        description="System-wide health and infrastructure events.\nRequires authentication; tenant-scoped updates.",
    )
    "System-wide health and infrastructure events.\nRequires authentication; tenant-scoped updates."


LiveSystemHealthLiveSystemHealth = SystemHealthEventDefault
LiveSystemHealth.model_rebuild()
