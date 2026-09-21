from datetime import datetime
from typing import Any, Optional

from pydantic import Field

from .base_model import BaseModel
from .enums import (
    DVRChapterMode,
    DVRChapterState,
    EventArtifactKind,
    IngestEndpointKind,
    IngestMode,
    MonitoringToggle,
    PlaybackPolicyType,
    RetentionSource,
    SigningKeyAlgorithm,
    SigningKeyStatus,
    StorageArtifactKind,
    StreamStatus,
    VodAssetStatus,
)


class AuthError(BaseModel):
    typename__: str = Field(alias="__typename")
    message: str
    code: Optional[str]


class EffectiveRetention(BaseModel):
    retention_days: int = Field(alias="retentionDays")
    retention_until: Optional[datetime] = Field(alias="retentionUntil")
    source: RetentionSource


class PlaybackPolicy(BaseModel):
    type_: PlaybackPolicyType = Field(alias="type")
    jwt: Optional["PlaybackPolicyJwt"]
    webhook: Optional["PlaybackPolicyWebhook"]


class PlaybackPolicyJwt(BaseModel):
    allowed_kids: list[str] = Field(alias="allowedKids")
    required_audience: list[str] = Field(alias="requiredAudience")
    required_claims_json: list["PlaybackPolicyJwtRequiredClaimsJson"] = Field(
        alias="requiredClaimsJson"
    )


class PlaybackPolicyJwtRequiredClaimsJson(BaseModel):
    name: str
    json_value: str = Field(alias="jsonValue")


class PlaybackPolicyWebhook(BaseModel):
    url: str
    timeout_ms: int = Field(alias="timeoutMs")
    secret_masked: str = Field(alias="secretMasked")


class ThumbnailAssets(BaseModel):
    poster_url: str = Field(alias="posterUrl")
    sprite_vtt_url: str = Field(alias="spriteVttUrl")
    sprite_jpg_url: str = Field(alias="spriteJpgUrl")
    asset_key: str = Field(alias="assetKey")


class Clip(BaseModel):
    typename__: str = Field(alias="__typename")
    id: str
    clip_hash: str = Field(alias="clipHash")
    playback_id: str = Field(alias="playbackId")
    stream_id: str = Field(alias="streamId")
    title: str
    description: Optional[str]
    start_time: int = Field(alias="startTime")
    duration: int
    size_bytes: Optional[float] = Field(alias="sizeBytes")
    status: str
    clip_mode: Optional[str] = Field(alias="clipMode")
    created_at: Optional[datetime] = Field(alias="createdAt")
    updated_at: Optional[datetime] = Field(alias="updatedAt")
    expires_at: Optional[datetime] = Field(alias="expiresAt")
    is_expired: bool = Field(alias="isExpired")
    playback_policy: Optional["ClipPlaybackPolicy"] = Field(alias="playbackPolicy")
    thumbnail_assets: Optional["ClipThumbnailAssets"] = Field(alias="thumbnailAssets")
    effective_retention: Optional["ClipEffectiveRetention"] = Field(
        alias="effectiveRetention"
    )


class ClipPlaybackPolicy(PlaybackPolicy):
    pass


class ClipThumbnailAssets(ThumbnailAssets):
    pass


class ClipEffectiveRetention(EffectiveRetention):
    pass


class DVRChapterRef(BaseModel):
    chapter_id: str = Field(alias="chapterId")
    mode: DVRChapterMode
    interval_seconds: Optional[int] = Field(alias="intervalSeconds")
    start_ms: float = Field(alias="startMs")
    end_ms: float = Field(alias="endMs")
    is_current: bool = Field(alias="isCurrent")
    state: DVRChapterState
    playback_id: Optional[str] = Field(alias="playbackId")
    has_gaps: bool = Field(alias="hasGaps")
    segment_count: int = Field(alias="segmentCount")
    last_failure_reason: Optional[str] = Field(alias="lastFailureReason")


class DVRRequest(BaseModel):
    typename__: str = Field(alias="__typename")
    id: Optional[str]
    dvr_hash: str = Field(alias="dvrHash")
    playback_id: str = Field(alias="playbackId")
    stream_id: str = Field(alias="streamId")
    title: Optional[str]
    status: Optional[str]
    created_at: datetime = Field(alias="createdAt")
    updated_at: datetime = Field(alias="updatedAt")
    started_at: Optional[datetime] = Field(alias="startedAt")
    ended_at: Optional[datetime] = Field(alias="endedAt")
    expires_at: Optional[datetime] = Field(alias="expiresAt")
    is_expired: bool = Field(alias="isExpired")
    duration_seconds: Optional[int] = Field(alias="durationSeconds")
    size_bytes: Optional[float] = Field(alias="sizeBytes")
    error_message: Optional[str] = Field(alias="errorMessage")


class DeleteSuccess(BaseModel):
    typename__: str = Field(alias="__typename")
    success: bool
    deleted_id: str = Field(alias="deletedId")
    pending: Optional[bool]


class DeveloperToken(BaseModel):
    typename__: str = Field(alias="__typename")
    id: str
    token_name: str = Field(alias="tokenName")
    token_value: Optional[str] = Field(alias="tokenValue")
    permissions: list[str]
    status: str
    last_used_at: Optional[datetime] = Field(alias="lastUsedAt")
    expires_at: Optional[datetime] = Field(alias="expiresAt")
    created_at: Optional[datetime] = Field(alias="createdAt")


class EventArtifact(BaseModel):
    artifact_id: str = Field(alias="artifactId")
    kind: EventArtifactKind
    stream_id: str = Field(alias="streamId")
    playback_id: str = Field(alias="playbackId")


class EventMoney(BaseModel):
    amount_minor: int = Field(alias="amountMinor")
    currency: str


class IngestEndpoint(BaseModel):
    node_id: str = Field(alias="nodeId")
    base_url: str = Field(alias="baseUrl")
    whip_url: Optional[str] = Field(alias="whipUrl")
    rtmp_url: Optional[str] = Field(alias="rtmpUrl")
    srt_url: Optional[str] = Field(alias="srtUrl")
    region: Optional[str]
    load_score: Optional[float] = Field(alias="loadScore")
    kind: IngestEndpointKind
    cluster_id: str = Field(alias="clusterId")


class NotFoundError(BaseModel):
    typename__: str = Field(alias="__typename")
    message: str
    code: Optional[str]
    resource_type: str = Field(alias="resourceType")
    resource_id: str = Field(alias="resourceId")


class PageInfo(BaseModel):
    start_cursor: Optional[str] = Field(alias="startCursor")
    end_cursor: Optional[str] = Field(alias="endCursor")
    has_next_page: bool = Field(alias="hasNextPage")
    has_previous_page: bool = Field(alias="hasPreviousPage")


class PushTarget(BaseModel):
    id: str
    stream_id: str = Field(alias="streamId")
    platform: Optional[str]
    name: str
    target_uri: str = Field(alias="targetUri")
    is_enabled: bool = Field(alias="isEnabled")
    status: str
    last_error: Optional[str] = Field(alias="lastError")
    reason_code: Optional[str] = Field(alias="reasonCode")
    last_pushed_at: Optional[datetime] = Field(alias="lastPushedAt")
    created_at: datetime = Field(alias="createdAt")


class RateLimitError(BaseModel):
    typename__: str = Field(alias="__typename")
    message: str
    code: Optional[str]
    retry_after: Optional[int] = Field(alias="retryAfter")


class SigningKey(BaseModel):
    typename__: str = Field(alias="__typename")
    id: str
    kid: str
    name: str
    algorithm: SigningKeyAlgorithm
    public_key_pem: str = Field(alias="publicKeyPem")
    status: SigningKeyStatus
    created_at: datetime = Field(alias="createdAt")
    last_used_at: Optional[datetime] = Field(alias="lastUsedAt")
    revoked_at: Optional[datetime] = Field(alias="revokedAt")


class StorageArtifact(BaseModel):
    key: str
    kind: StorageArtifactKind
    id: str
    hash: str
    playback_id: Optional[str] = Field(alias="playbackId")
    stream_id: Optional[str] = Field(alias="streamId")
    stream_title: str = Field(alias="streamTitle")
    title: str
    description: Optional[str]
    error_message: Optional[str] = Field(alias="errorMessage")
    size_bytes: Optional[float] = Field(alias="sizeBytes")
    status: str
    created_at: datetime = Field(alias="createdAt")
    updated_at: datetime = Field(alias="updatedAt")
    expires_at: Optional[datetime] = Field(alias="expiresAt")
    delete_id: str = Field(alias="deleteId")
    duration_seconds: Optional[float] = Field(alias="durationSeconds")
    thumbnail_assets: Optional["StorageArtifactThumbnailAssets"] = Field(
        alias="thumbnailAssets"
    )


class StorageArtifactThumbnailAssets(ThumbnailAssets):
    pass


class Stream(BaseModel):
    typename__: str = Field(alias="__typename")
    id: str
    stream_id: str = Field(alias="streamId")
    name: str
    description: Optional[str]
    stream_key: Optional[str] = Field(alias="streamKey")
    playback_id: str = Field(alias="playbackId")
    record: bool
    ingest_mode: IngestMode = Field(alias="ingestMode")
    pull_source: Optional["StreamPullSource"] = Field(alias="pullSource")
    created_at: datetime = Field(alias="createdAt")
    updated_at: datetime = Field(alias="updatedAt")
    dvr_chapter_mode: Optional[DVRChapterMode] = Field(alias="dvrChapterMode")
    dvr_chapter_interval_seconds: Optional[int] = Field(
        alias="dvrChapterIntervalSeconds"
    )
    monitoring: MonitoringToggle
    playback_policy: Optional["StreamPlaybackPolicy"] = Field(alias="playbackPolicy")
    metrics: Optional["StreamMetrics"]


class StreamPullSource(BaseModel):
    source_uri_redacted: str = Field(alias="sourceUriRedacted")
    enabled: bool
    class_: str = Field(alias="class")


class StreamPlaybackPolicy(PlaybackPolicy):
    pass


class StreamMetrics(BaseModel):
    status: StreamStatus
    is_live: bool = Field(alias="isLive")
    current_viewers: int = Field(alias="currentViewers")
    started_at: Optional[datetime] = Field(alias="startedAt")
    updated_at: datetime = Field(alias="updatedAt")


class StreamKey(BaseModel):
    typename__: str = Field(alias="__typename")
    id: str
    stream_id: str = Field(alias="streamId")
    key_value: str = Field(alias="keyValue")
    key_name: Optional[str] = Field(alias="keyName")
    is_active: bool = Field(alias="isActive")
    last_used_at: Optional[datetime] = Field(alias="lastUsedAt")
    created_at: datetime = Field(alias="createdAt")


class ValidationError(BaseModel):
    typename__: str = Field(alias="__typename")
    message: str
    code: Optional[str]
    field: Optional[str]
    constraint: Optional[str]


class ViewerEndpoint(BaseModel):
    node_id: str = Field(alias="nodeId")
    base_url: str = Field(alias="baseUrl")
    protocol: str
    url: str
    geo_distance: Optional[float] = Field(alias="geoDistance")
    load_score: Optional[float] = Field(alias="loadScore")
    outputs: Optional[Any]


class VodAsset(BaseModel):
    typename__: str = Field(alias="__typename")
    id: str
    artifact_hash: str = Field(alias="artifactHash")
    playback_id: str = Field(alias="playbackId")
    stream_id: Optional[str] = Field(alias="streamId")
    title: Optional[str]
    description: Optional[str]
    filename: Optional[str]
    status: VodAssetStatus
    size_bytes: Optional[float] = Field(alias="sizeBytes")
    duration_ms: Optional[int] = Field(alias="durationMs")
    resolution: Optional[str]
    video_codec: Optional[str] = Field(alias="videoCodec")
    audio_codec: Optional[str] = Field(alias="audioCodec")
    bitrate_kbps: Optional[int] = Field(alias="bitrateKbps")
    created_at: datetime = Field(alias="createdAt")
    updated_at: datetime = Field(alias="updatedAt")
    expires_at: Optional[datetime] = Field(alias="expiresAt")
    error_message: Optional[str] = Field(alias="errorMessage")
    playback_policy: Optional["VodAssetPlaybackPolicy"] = Field(alias="playbackPolicy")
    thumbnail_assets: Optional["VodAssetThumbnailAssets"] = Field(
        alias="thumbnailAssets"
    )
    effective_retention: Optional["VodAssetEffectiveRetention"] = Field(
        alias="effectiveRetention"
    )


class VodAssetPlaybackPolicy(PlaybackPolicy):
    pass


class VodAssetThumbnailAssets(ThumbnailAssets):
    pass


class VodAssetEffectiveRetention(EffectiveRetention):
    pass


AuthError.model_rebuild()
EffectiveRetention.model_rebuild()
PlaybackPolicy.model_rebuild()
ThumbnailAssets.model_rebuild()
Clip.model_rebuild()
DVRChapterRef.model_rebuild()
DVRRequest.model_rebuild()
DeleteSuccess.model_rebuild()
DeveloperToken.model_rebuild()
EventArtifact.model_rebuild()
EventMoney.model_rebuild()
IngestEndpoint.model_rebuild()
NotFoundError.model_rebuild()
PageInfo.model_rebuild()
PushTarget.model_rebuild()
RateLimitError.model_rebuild()
SigningKey.model_rebuild()
StorageArtifact.model_rebuild()
Stream.model_rebuild()
StreamKey.model_rebuild()
ValidationError.model_rebuild()
ViewerEndpoint.model_rebuild()
VodAsset.model_rebuild()
