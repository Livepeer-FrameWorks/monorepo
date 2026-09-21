from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import (  # noqa: F401
    VodAsset,
    VodAssetEffectiveRetention,
    VodAssetPlaybackPolicy,
    VodAssetThumbnailAssets,
)


class GetVodAsset(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    vod_asset: Optional["GetVodAssetVodAsset"] = Field(
        alias="vodAsset", description="Fetch a single VOD asset by ID."
    )
    "Fetch a single VOD asset by ID."


class GetVodAssetVodAsset(VodAsset):
    """A Video-on-Demand asset uploaded by the tenant.
    VOD assets can be played back using the playbackId in playback URLs."""

    pass


GetVodAsset.model_rebuild()
