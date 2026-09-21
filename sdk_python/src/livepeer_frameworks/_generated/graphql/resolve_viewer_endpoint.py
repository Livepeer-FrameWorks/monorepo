from datetime import datetime
from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import ThumbnailAssets, ViewerEndpoint


class ResolveViewerEndpoint(BaseModel):
    resolve_viewer_endpoint: Optional["ResolveViewerEndpointResolveViewerEndpoint"] = (
        Field(alias="resolveViewerEndpoint")
    )


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
    telemetry_token: Optional[str] = Field(alias="telemetryToken")
    thumbnail_assets: Optional[
        "ResolveViewerEndpointResolveViewerEndpointMetadataThumbnailAssets"
    ] = Field(alias="thumbnailAssets")


ResolveViewerEndpointResolveViewerEndpointMetadataThumbnailAssets = ThumbnailAssets
ResolveViewerEndpoint.model_rebuild()
ResolveViewerEndpointResolveViewerEndpoint.model_rebuild()
ResolveViewerEndpointResolveViewerEndpointMetadata.model_rebuild()
