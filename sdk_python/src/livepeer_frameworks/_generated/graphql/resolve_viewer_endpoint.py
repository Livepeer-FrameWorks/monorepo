from datetime import datetime
from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import ThumbnailAssets, ViewerEndpoint


class ResolveViewerEndpoint(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    resolve_viewer_endpoint: Optional["ResolveViewerEndpointResolveViewerEndpoint"] = (
        Field(
            alias="resolveViewerEndpoint",
            description="Resolve a playback ID to viewer endpoints (HLS, DASH, etc.).\nUsed by players to get the optimal CDN endpoint for playback.",
        )
    )
    "Resolve a playback ID to viewer endpoints (HLS, DASH, etc.).\nUsed by players to get the optimal CDN endpoint for playback."


class ResolveViewerEndpointResolveViewerEndpoint(BaseModel):
    primary: Optional["ResolveViewerEndpointResolveViewerEndpointPrimary"]
    fallbacks: list["ResolveViewerEndpointResolveViewerEndpointFallbacks"]
    metadata: Optional["ResolveViewerEndpointResolveViewerEndpointMetadata"]


ResolveViewerEndpointResolveViewerEndpointPrimary = ViewerEndpoint
ResolveViewerEndpointResolveViewerEndpointFallbacks = ViewerEndpoint


class ResolveViewerEndpointResolveViewerEndpointMetadata(BaseModel):
    content_type: str = Field(alias="contentType")
    content_id: str = Field(alias="contentId")
    title: Optional[str]
    description: Optional[str]
    duration_seconds: Optional[int] = Field(alias="durationSeconds")
    status: str
    is_live: bool = Field(alias="isLive")
    viewers: int
    recording_size_bytes: Optional[float] = Field(alias="recordingSizeBytes")
    clip_source: Optional[str] = Field(alias="clipSource")
    created_at: Optional[datetime] = Field(alias="createdAt")
    telemetry_token: Optional[str] = Field(
        alias="telemetryToken",
        description="Short-lived signed token binding the resolved serving endpoint. The player\nechoes it on its boot-telemetry beacon so Bridge can trust cluster attribution.\nInfrastructure attribution only — carries no viewer identity.",
    )
    "Short-lived signed token binding the resolved serving endpoint. The player\nechoes it on its boot-telemetry beacon so Bridge can trust cluster attribution.\nInfrastructure attribution only — carries no viewer identity."
    thumbnail_assets: Optional[
        "ResolveViewerEndpointResolveViewerEndpointMetadataThumbnailAssets"
    ] = Field(alias="thumbnailAssets")


class ResolveViewerEndpointResolveViewerEndpointMetadataThumbnailAssets(
    ThumbnailAssets
):
    """Chandler-served thumbnail asset URLs (poster, sprite, VTT cues). URL shape
    is `{chandlerBase}/assets/{assetKey}/poster.jpg`,
    `/assets/{assetKey}/sprite.jpg`, `/assets/{assetKey}/sprite.vtt` — Chandler
    serves the object key directly, no version resolution. assetKey is stream_id
    for live streams; clip_hash / dvr_hash / vod_hash (= artifact_hash) for artifacts."""

    pass


ResolveViewerEndpoint.model_rebuild()
ResolveViewerEndpointResolveViewerEndpoint.model_rebuild()
ResolveViewerEndpointResolveViewerEndpointMetadata.model_rebuild()
