from pydantic import Field

from .base_model import BaseModel
from .fragments import IncidentUpdatedEventDefault


class LiveIncidentUpdates(BaseModel):
    """Real-time subscriptions for live event streaming via WebSocket.
    All subscriptions are tenant-scoped and require authentication.
    Events are delivered as they occur with minimal latency."""

    live_incident_updates: "LiveIncidentUpdatesLiveIncidentUpdates" = Field(
        alias="liveIncidentUpdates",
        description="Changes to incidents on clusters the current tenant owns. For platform\noperators, changes to every incident: platform scope and every tenant.",
    )
    "Changes to incidents on clusters the current tenant owns. For platform\noperators, changes to every incident: platform scope and every tenant."


class LiveIncidentUpdatesLiveIncidentUpdates(IncidentUpdatedEventDefault):
    """Change to an incident the subscriber can see."""

    pass


LiveIncidentUpdates.model_rebuild()
