from pydantic import Field

from .base_model import BaseModel
from .fragments import PageInfoDefault, VodRetentionAssetDefault


class GetVodRetentionAssets(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetVodRetentionAssetsAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetVodRetentionAssetsAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    health: "GetVodRetentionAssetsAnalyticsHealth" = Field(
        description="Health analytics: stream quality, rebuffering, client QoE."
    )
    "Health analytics: stream quality, rebuffering, client QoE."


class GetVodRetentionAssetsAnalyticsHealth(BaseModel):
    """Health analytics for streams.
    `streamId` arguments accept Stream.id (Relay global ID)."""

    vod_retention_assets: "GetVodRetentionAssetsAnalyticsHealthVodRetentionAssets" = Field(
        alias="vodRetentionAssets",
        description="VOD assets that have retention data in the window — the picker backing the\nretention view. Eligibility (VOD content with real reach samples) is owned by\nanalytics; title/playbackId are composed from the catalog by artifactHash.",
    )
    "VOD assets that have retention data in the window — the picker backing the\nretention view. Eligibility (VOD content with real reach samples) is owned by\nanalytics; title/playbackId are composed from the catalog by artifactHash."


class GetVodRetentionAssetsAnalyticsHealthVodRetentionAssets(BaseModel):
    edges: list["GetVodRetentionAssetsAnalyticsHealthVodRetentionAssetsEdges"]
    page_info: "GetVodRetentionAssetsAnalyticsHealthVodRetentionAssetsPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


class GetVodRetentionAssetsAnalyticsHealthVodRetentionAssetsEdges(BaseModel):
    cursor: str
    node: "GetVodRetentionAssetsAnalyticsHealthVodRetentionAssetsEdgesNode"


class GetVodRetentionAssetsAnalyticsHealthVodRetentionAssetsEdgesNode(
    VodRetentionAssetDefault
):
    """A VOD asset with retention data in the window. Eligibility + stats (sessions,
    duration, lastSeen) come from analytics; title/playbackId are composed from the
    catalog by artifactHash — both may be null when the asset is uncatalogued (e.g.
    deleted but retention still within TTL)."""

    pass


GetVodRetentionAssetsAnalyticsHealthVodRetentionAssetsPageInfo = PageInfoDefault
GetVodRetentionAssets.model_rebuild()
GetVodRetentionAssetsAnalytics.model_rebuild()
GetVodRetentionAssetsAnalyticsHealth.model_rebuild()
GetVodRetentionAssetsAnalyticsHealthVodRetentionAssets.model_rebuild()
GetVodRetentionAssetsAnalyticsHealthVodRetentionAssetsEdges.model_rebuild()
