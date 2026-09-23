from pydantic import Field

from .base_model import BaseModel
from .fragments import TopAssetEntryDefault


class GetTopAssets(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetTopAssetsAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetTopAssetsAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    health: "GetTopAssetsAnalyticsHealth" = Field(
        description="Health analytics: stream quality, rebuffering, client QoE."
    )
    "Health analytics: stream quality, rebuffering, client QoE."


class GetTopAssetsAnalyticsHealth(BaseModel):
    """Health analytics for streams.
    `streamId` arguments accept Stream.id (Relay global ID)."""

    top_assets: list["GetTopAssetsAnalyticsHealthTopAssets"] = Field(
        alias="topAssets",
        description='Top assets by audience in the window — server-ranked by sessions, across every kind\n(VOD, clip, DVR, chapter). Backs the "Top Assets" table on the analytics overview.\ntitle/playbackId are composed from the catalog by artifactHash.',
    )
    'Top assets by audience in the window — server-ranked by sessions, across every kind\n(VOD, clip, DVR, chapter). Backs the "Top Assets" table on the analytics overview.\ntitle/playbackId are composed from the catalog by artifactHash.'


class GetTopAssetsAnalyticsHealthTopAssets(TopAssetEntryDefault):
    """One ranked asset for the Top Assets surface — cross-kind, ranked server-side by
    audience sessions in the window. `kind` badges the asset type; title/playbackId are
    composed from the catalog. (Named distinctly from the periscope proto TopAsset to
    avoid gqlgen autobinding to that message.)"""

    pass


GetTopAssets.model_rebuild()
GetTopAssetsAnalytics.model_rebuild()
GetTopAssetsAnalyticsHealth.model_rebuild()
