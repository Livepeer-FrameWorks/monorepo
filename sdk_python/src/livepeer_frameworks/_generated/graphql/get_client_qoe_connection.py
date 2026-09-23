from pydantic import Field

from .base_model import BaseModel
from .fragments import ClientMetrics5mDefault, PageInfoDefault


class GetClientQoeConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetClientQoeConnectionAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetClientQoeConnectionAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    health: "GetClientQoeConnectionAnalyticsHealth" = Field(
        description="Health analytics: stream quality, rebuffering, client QoE."
    )
    "Health analytics: stream quality, rebuffering, client QoE."


class GetClientQoeConnectionAnalyticsHealth(BaseModel):
    """Health analytics for streams.
    `streamId` arguments accept Stream.id (Relay global ID)."""

    client_qoe_connection: "GetClientQoeConnectionAnalyticsHealthClientQoeConnection" = Field(
        alias="clientQoeConnection"
    )


class GetClientQoeConnectionAnalyticsHealthClientQoeConnection(BaseModel):
    edges: list["GetClientQoeConnectionAnalyticsHealthClientQoeConnectionEdges"]
    page_info: "GetClientQoeConnectionAnalyticsHealthClientQoeConnectionPageInfo" = (
        Field(alias="pageInfo")
    )
    total_count: int = Field(alias="totalCount")


class GetClientQoeConnectionAnalyticsHealthClientQoeConnectionEdges(BaseModel):
    cursor: str
    node: "GetClientQoeConnectionAnalyticsHealthClientQoeConnectionEdgesNode"


GetClientQoeConnectionAnalyticsHealthClientQoeConnectionEdgesNode = (
    ClientMetrics5mDefault
)
GetClientQoeConnectionAnalyticsHealthClientQoeConnectionPageInfo = PageInfoDefault
GetClientQoeConnection.model_rebuild()
GetClientQoeConnectionAnalytics.model_rebuild()
GetClientQoeConnectionAnalyticsHealth.model_rebuild()
GetClientQoeConnectionAnalyticsHealthClientQoeConnection.model_rebuild()
GetClientQoeConnectionAnalyticsHealthClientQoeConnectionEdges.model_rebuild()
