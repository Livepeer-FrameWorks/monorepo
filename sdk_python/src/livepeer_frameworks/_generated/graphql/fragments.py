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


class AuthErrorFields(BaseModel):
    typename__: str = Field(alias="__typename")
    message: str
    code: Optional[str]


class EffectiveRetentionFields(BaseModel):
    retention_days: int = Field(alias="retentionDays")
    retention_until: Optional[datetime] = Field(alias="retentionUntil")
    source: RetentionSource


class PlaybackPolicyFields(BaseModel):
    type_: PlaybackPolicyType = Field(alias="type")
    jwt: Optional["PlaybackPolicyFieldsJwt"]
    webhook: Optional["PlaybackPolicyFieldsWebhook"]


class PlaybackPolicyFieldsJwt(BaseModel):
    allowed_kids: list[str] = Field(alias="allowedKids")
    required_audience: list[str] = Field(alias="requiredAudience")
    required_claims_json: list["PlaybackPolicyFieldsJwtRequiredClaimsJson"] = Field(
        alias="requiredClaimsJson"
    )


class PlaybackPolicyFieldsJwtRequiredClaimsJson(BaseModel):
    name: str
    json_value: str = Field(alias="jsonValue")


class PlaybackPolicyFieldsWebhook(BaseModel):
    url: str
    timeout_ms: int = Field(alias="timeoutMs")
    secret_masked: str = Field(alias="secretMasked")


class ThumbnailAssetsFields(BaseModel):
    poster_url: str = Field(alias="posterUrl")
    sprite_vtt_url: str = Field(alias="spriteVttUrl")
    sprite_jpg_url: str = Field(alias="spriteJpgUrl")
    asset_key: str = Field(alias="assetKey")


class ClipFields(BaseModel):
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
    playback_policy: Optional["ClipFieldsPlaybackPolicy"] = Field(
        alias="playbackPolicy"
    )
    thumbnail_assets: Optional["ClipFieldsThumbnailAssets"] = Field(
        alias="thumbnailAssets"
    )
    effective_retention: Optional["ClipFieldsEffectiveRetention"] = Field(
        alias="effectiveRetention"
    )


class ClipFieldsPlaybackPolicy(PlaybackPolicyFields):
    pass


class ClipFieldsThumbnailAssets(ThumbnailAssetsFields):
    pass


class ClipFieldsEffectiveRetention(EffectiveRetentionFields):
    pass


class DVRChapterRefFields(BaseModel):
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


class DVRRequestFields(BaseModel):
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


class DeleteSuccessFields(BaseModel):
    typename__: str = Field(alias="__typename")
    success: bool
    deleted_id: str = Field(alias="deletedId")
    pending: Optional[bool]


class DeveloperTokenFields(BaseModel):
    typename__: str = Field(alias="__typename")
    id: str
    token_name: str = Field(alias="tokenName")
    token_value: Optional[str] = Field(alias="tokenValue")
    permissions: list[str]
    status: str
    last_used_at: Optional[datetime] = Field(alias="lastUsedAt")
    expires_at: Optional[datetime] = Field(alias="expiresAt")
    created_at: Optional[datetime] = Field(alias="createdAt")


class EventArtifactFields(BaseModel):
    artifact_id: str = Field(alias="artifactId")
    kind: EventArtifactKind
    stream_id: str = Field(alias="streamId")
    playback_id: str = Field(alias="playbackId")


class EventMoneyFields(BaseModel):
    amount_minor: int = Field(alias="amountMinor")
    currency: str


class IngestEndpointFields(BaseModel):
    node_id: str = Field(alias="nodeId")
    base_url: str = Field(alias="baseUrl")
    whip_url: Optional[str] = Field(alias="whipUrl")
    rtmp_url: Optional[str] = Field(alias="rtmpUrl")
    srt_url: Optional[str] = Field(alias="srtUrl")
    region: Optional[str]
    load_score: Optional[float] = Field(alias="loadScore")
    kind: IngestEndpointKind
    cluster_id: str = Field(alias="clusterId")


class NotFoundErrorFields(BaseModel):
    typename__: str = Field(alias="__typename")
    message: str
    code: Optional[str]
    resource_type: str = Field(alias="resourceType")
    resource_id: str = Field(alias="resourceId")


class PageInfoFields(BaseModel):
    start_cursor: Optional[str] = Field(alias="startCursor")
    end_cursor: Optional[str] = Field(alias="endCursor")
    has_next_page: bool = Field(alias="hasNextPage")
    has_previous_page: bool = Field(alias="hasPreviousPage")


class PushTargetFields(BaseModel):
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


class RateLimitErrorFields(BaseModel):
    typename__: str = Field(alias="__typename")
    message: str
    code: Optional[str]
    retry_after: Optional[int] = Field(alias="retryAfter")


class SigningKeyFields(BaseModel):
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


class StorageArtifactFields(BaseModel):
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
    thumbnail_assets: Optional["StorageArtifactFieldsThumbnailAssets"] = Field(
        alias="thumbnailAssets"
    )


class StorageArtifactFieldsThumbnailAssets(ThumbnailAssetsFields):
    pass


class StreamFields(BaseModel):
    typename__: str = Field(alias="__typename")
    id: str
    stream_id: str = Field(alias="streamId")
    name: str
    description: Optional[str]
    stream_key: Optional[str] = Field(alias="streamKey")
    playback_id: str = Field(alias="playbackId")
    record: bool
    ingest_mode: IngestMode = Field(alias="ingestMode")
    pull_source: Optional["StreamFieldsPullSource"] = Field(alias="pullSource")
    created_at: datetime = Field(alias="createdAt")
    updated_at: datetime = Field(alias="updatedAt")
    dvr_chapter_mode: Optional[DVRChapterMode] = Field(alias="dvrChapterMode")
    dvr_chapter_interval_seconds: Optional[int] = Field(
        alias="dvrChapterIntervalSeconds"
    )
    monitoring: MonitoringToggle
    playback_policy: Optional["StreamFieldsPlaybackPolicy"] = Field(
        alias="playbackPolicy"
    )
    metrics: Optional["StreamFieldsMetrics"]


class StreamFieldsPullSource(BaseModel):
    source_uri_redacted: str = Field(alias="sourceUriRedacted")
    enabled: bool
    class_: str = Field(alias="class")


class StreamFieldsPlaybackPolicy(PlaybackPolicyFields):
    pass


class StreamFieldsMetrics(BaseModel):
    status: StreamStatus
    is_live: bool = Field(alias="isLive")
    current_viewers: int = Field(alias="currentViewers")
    started_at: Optional[datetime] = Field(alias="startedAt")
    updated_at: datetime = Field(alias="updatedAt")


class StreamKeyFields(BaseModel):
    typename__: str = Field(alias="__typename")
    id: str
    stream_id: str = Field(alias="streamId")
    key_value: str = Field(alias="keyValue")
    key_name: Optional[str] = Field(alias="keyName")
    is_active: bool = Field(alias="isActive")
    last_used_at: Optional[datetime] = Field(alias="lastUsedAt")
    created_at: datetime = Field(alias="createdAt")


class ValidationErrorFields(BaseModel):
    typename__: str = Field(alias="__typename")
    message: str
    code: Optional[str]
    field: Optional[str]
    constraint: Optional[str]


class ViewerEndpointFields(BaseModel):
    node_id: str = Field(alias="nodeId")
    base_url: str = Field(alias="baseUrl")
    protocol: str
    url: str
    geo_distance: Optional[float] = Field(alias="geoDistance")
    load_score: Optional[float] = Field(alias="loadScore")
    outputs: Optional[Any]


class VodAssetFields(BaseModel):
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
    playback_policy: Optional["VodAssetFieldsPlaybackPolicy"] = Field(
        alias="playbackPolicy"
    )
    thumbnail_assets: Optional["VodAssetFieldsThumbnailAssets"] = Field(
        alias="thumbnailAssets"
    )
    effective_retention: Optional["VodAssetFieldsEffectiveRetention"] = Field(
        alias="effectiveRetention"
    )


class VodAssetFieldsPlaybackPolicy(PlaybackPolicyFields):
    pass


class VodAssetFieldsThumbnailAssets(ThumbnailAssetsFields):
    pass


class VodAssetFieldsEffectiveRetention(EffectiveRetentionFields):
    pass


AuthErrorFields.model_rebuild()
EffectiveRetentionFields.model_rebuild()
PlaybackPolicyFields.model_rebuild()
ThumbnailAssetsFields.model_rebuild()
ClipFields.model_rebuild()
DVRChapterRefFields.model_rebuild()
DVRRequestFields.model_rebuild()
DeleteSuccessFields.model_rebuild()
DeveloperTokenFields.model_rebuild()
EventArtifactFields.model_rebuild()
EventMoneyFields.model_rebuild()
IngestEndpointFields.model_rebuild()
NotFoundErrorFields.model_rebuild()
PageInfoFields.model_rebuild()
PushTargetFields.model_rebuild()
RateLimitErrorFields.model_rebuild()
SigningKeyFields.model_rebuild()
StorageArtifactFields.model_rebuild()
StreamFields.model_rebuild()
StreamKeyFields.model_rebuild()
ValidationErrorFields.model_rebuild()
ViewerEndpointFields.model_rebuild()
VodAssetFields.model_rebuild()
