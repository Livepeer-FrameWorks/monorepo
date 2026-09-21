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
    """Resolved retention horizon for a single asset (DVR, clip, or VOD).
    Embedded on Clip, DVRRequest, and VodAsset."""

    retention_days: int = Field(
        alias="retentionDays",
        description="Days from now until the artifact is scheduled for deletion. 0 = no\nauto-expire (retentionUntil is null).",
    )
    "Days from now until the artifact is scheduled for deletion. 0 = no\nauto-expire (retentionUntil is null)."
    retention_until: Optional[datetime] = Field(
        alias="retentionUntil",
        description="Scheduled deletion timestamp. Null when the artifact has no horizon (kept forever).",
    )
    "Scheduled deletion timestamp. Null when the artifact has no horizon (kept forever)."
    source: RetentionSource


class PlaybackPolicy(BaseModel):
    """Per-playback-object access policy. Foghorn reads this in the USER_NEW
    trigger handler; the requiresAuth marker (on the playback object itself)
    gates whether the full policy is fetched at all."""

    type_: PlaybackPolicyType = Field(alias="type")
    jwt: Optional["PlaybackPolicyJwt"] = Field(
        description="JWT-policy details, populated when type == JWT."
    )
    "JWT-policy details, populated when type == JWT."
    webhook: Optional["PlaybackPolicyWebhook"] = Field(
        description="Webhook-policy details, populated when type == WEBHOOK. Secret is masked."
    )
    "Webhook-policy details, populated when type == WEBHOOK. Secret is masked."


class PlaybackPolicyJwt(BaseModel):
    """JWT-gated playback policy. The viewer attaches a JWT minted with one of the
    allowed signing keys; FrameWorks verifies signature, alg=ES256, exp/iat/nbf,
    and any required audience / claim constraints."""

    allowed_kids: list[str] = Field(
        alias="allowedKids",
        description="Allowed signing key IDs. Empty = any active tenant key.",
    )
    "Allowed signing key IDs. Empty = any active tenant key."
    required_audience: list[str] = Field(
        alias="requiredAudience",
        description="If set, the viewer JWT's `aud` claim must contain at least one of these.",
    )
    "If set, the viewer JWT's `aud` claim must contain at least one of these."
    required_claims_json: list["PlaybackPolicyJwtRequiredClaimsJson"] = Field(
        alias="requiredClaimsJson",
        description="Required claim constraints. Each value is the JSON-encoded representation\nof the expected claim value (so callers can require strings, numbers,\nbooleans, or arrays consistently). Empty = no claim check.",
    )
    "Required claim constraints. Each value is the JSON-encoded representation\nof the expected claim value (so callers can require strings, numbers,\nbooleans, or arrays consistently). Empty = no claim check."


class PlaybackPolicyJwtRequiredClaimsJson(BaseModel):
    """One required-claim constraint, JSON-encoded for type-flexible matching."""

    name: str
    json_value: str = Field(
        alias="jsonValue",
        description='JSON-encoded expected value (e.g. `"pro"`, `true`, `42`, `["a","b"]`).',
    )
    'JSON-encoded expected value (e.g. `"pro"`, `true`, `42`, `["a","b"]`).'


class PlaybackPolicyWebhook(BaseModel):
    """Webhook-callback playback policy. On every viewer connect, FrameWorks POSTs
    to the URL with an HMAC-signed body and reads allow/deny from the response.
    The secret is write-only on input and is never returned by queries."""

    url: str
    timeout_ms: int = Field(
        alias="timeoutMs",
        description="Outbound POST timeout in milliseconds. Capped server-side at 10000.",
    )
    "Outbound POST timeout in milliseconds. Capped server-side at 10000."
    secret_masked: str = Field(
        alias="secretMasked",
        description="Always 'redacted' on read; the actual secret is fieldcrypt-encrypted at rest.",
    )
    "Always 'redacted' on read; the actual secret is fieldcrypt-encrypted at rest."


class ThumbnailAssets(BaseModel):
    """Chandler-served thumbnail asset URLs (poster, sprite, VTT cues). URL shape
    is `{chandlerBase}/assets/{assetKey}/poster.jpg`,
    `/assets/{assetKey}/sprite.jpg`, `/assets/{assetKey}/sprite.vtt` — Chandler
    serves the object key directly, no version resolution. assetKey is stream_id
    for live streams; clip_hash / dvr_hash / vod_hash (= artifact_hash) for artifacts."""

    poster_url: str = Field(alias="posterUrl")
    sprite_vtt_url: str = Field(alias="spriteVttUrl")
    sprite_jpg_url: str = Field(alias="spriteJpgUrl")
    asset_key: str = Field(alias="assetKey")


class Clip(BaseModel):
    """A video clip extracted from a live stream's DVR buffer.
    Clips are created from recorded stream segments and stored for playback."""

    typename__: str = Field(alias="__typename")
    id: str = Field(description="Global unique identifier for Relay compatibility.")
    "Global unique identifier for Relay compatibility."
    clip_hash: str = Field(
        alias="clipHash", description="Internal clip hash for storage and playback."
    )
    "Internal clip hash for storage and playback."
    playback_id: str = Field(
        alias="playbackId",
        description="Public playback identifier for generating playback URLs.",
    )
    "Public playback identifier for generating playback URLs."
    stream_id: str = Field(
        alias="streamId", description="Stream this clip was created from."
    )
    "Stream this clip was created from."
    title: str = Field(description="Display title for the clip.")
    "Display title for the clip."
    description: Optional[str] = Field(
        description="Optional description of clip content."
    )
    "Optional description of clip content."
    start_time: int = Field(
        alias="startTime", description="Resolved start time (Unix seconds)."
    )
    "Resolved start time (Unix seconds)."
    duration: int = Field(description="Clip duration in seconds.")
    "Clip duration in seconds."
    size_bytes: Optional[float] = Field(
        alias="sizeBytes", description="File size in bytes."
    )
    "File size in bytes."
    status: str = Field(
        description="Processing status (queued, processing, ready, failed)."
    )
    "Processing status (queued, processing, ready, failed)."
    clip_mode: Optional[str] = Field(
        alias="clipMode",
        description="Clip creation mode (ABSOLUTE, RELATIVE, DURATION, CLIP_NOW).",
    )
    "Clip creation mode (ABSOLUTE, RELATIVE, DURATION, CLIP_NOW)."
    created_at: Optional[datetime] = Field(
        alias="createdAt", description="When the clip was requested."
    )
    "When the clip was requested."
    updated_at: Optional[datetime] = Field(
        alias="updatedAt", description="When the clip was last updated."
    )
    "When the clip was last updated."
    expires_at: Optional[datetime] = Field(
        alias="expiresAt", description="When the clip will be auto-deleted."
    )
    "When the clip will be auto-deleted."
    is_expired: bool = Field(
        alias="isExpired",
        description="Whether the clip has passed its retention date (expiresAt < now).",
    )
    "Whether the clip has passed its retention date (expiresAt < now)."
    playback_policy: Optional["ClipPlaybackPolicy"] = Field(
        alias="playbackPolicy",
        description="Playback access policy snapshotted at clip creation. null/PUBLIC means\nanyone with the playbackId can watch. Independent from the source stream's\npolicy after creation — flipping the source stream's policy does not affect\nalready-shared clip URLs.",
    )
    "Playback access policy snapshotted at clip creation. null/PUBLIC means\nanyone with the playbackId can watch. Independent from the source stream's\npolicy after creation — flipping the source stream's policy does not affect\nalready-shared clip URLs."
    thumbnail_assets: Optional["ClipThumbnailAssets"] = Field(
        alias="thumbnailAssets",
        description="Server-resolved Chandler URLs for the clip's poster and sprite thumbnails.\nNull until Foghorn confirms the thumbnail upload.",
    )
    "Server-resolved Chandler URLs for the clip's poster and sprite thumbnails.\nNull until Foghorn confirms the thumbnail upload."
    effective_retention: Optional["ClipEffectiveRetention"] = Field(
        alias="effectiveRetention",
        description="Resolved retention horizon with the source of the decision (per-asset\noverride → per-stream override → tenant default → tier entitlement).\nNull when retention_until is unset (infinite).",
    )
    "Resolved retention horizon with the source of the decision (per-asset\noverride → per-stream override → tenant default → tier entitlement).\nNull when retention_until is unset (infinite)."


class ClipPlaybackPolicy(PlaybackPolicy):
    """Per-playback-object access policy. Foghorn reads this in the USER_NEW
    trigger handler; the requiresAuth marker (on the playback object itself)
    gates whether the full policy is fetched at all."""

    pass


class ClipThumbnailAssets(ThumbnailAssets):
    """Chandler-served thumbnail asset URLs (poster, sprite, VTT cues). URL shape
    is `{chandlerBase}/assets/{assetKey}/poster.jpg`,
    `/assets/{assetKey}/sprite.jpg`, `/assets/{assetKey}/sprite.vtt` — Chandler
    serves the object key directly, no version resolution. assetKey is stream_id
    for live streams; clip_hash / dvr_hash / vod_hash (= artifact_hash) for artifacts."""

    pass


class ClipEffectiveRetention(EffectiveRetention):
    """Resolved retention horizon for a single asset (DVR, clip, or VOD).
    Embedded on Clip, DVRRequest, and VodAsset."""

    pass


class DVRChapterRef(BaseModel):
    """A reference to a chapter for the chapter list UI. Same shape as
    DVRChapter without the timeline-zero derivations."""

    chapter_id: str = Field(alias="chapterId")
    mode: DVRChapterMode
    interval_seconds: Optional[int] = Field(alias="intervalSeconds")
    start_ms: float = Field(alias="startMs")
    end_ms: float = Field(alias="endMs")
    is_current: bool = Field(alias="isCurrent")
    state: DVRChapterState
    playback_id: Optional[str] = Field(
        alias="playbackId",
        description="Public playback key minted by Commodore; null until finalization dispatches.",
    )
    "Public playback key minted by Commodore; null until finalization dispatches."
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
    is_expired: bool = Field(
        alias="isExpired",
        description="Whether the DVR has passed its retention date (expiresAt < now).",
    )
    "Whether the DVR has passed its retention date (expiresAt < now)."
    duration_seconds: Optional[int] = Field(alias="durationSeconds")
    size_bytes: Optional[float] = Field(alias="sizeBytes")
    error_message: Optional[str] = Field(alias="errorMessage")


class DeleteSuccess(BaseModel):
    typename__: str = Field(alias="__typename")
    success: bool
    deleted_id: str = Field(alias="deletedId")
    pending: Optional[bool] = Field(
        description="True when the delete was accepted but is NOT yet finalized (e.g. a stream deletion awaiting the serving cell's\ncleanup-tombstone acknowledgement). The operation converges asynchronously; false means fully deleted."
    )
    "True when the delete was accepted but is NOT yet finalized (e.g. a stream deletion awaiting the serving cell's\ncleanup-tombstone acknowledgement). The operation converges asynchronously; false means fully deleted."


class DeveloperToken(BaseModel):
    """An API token for programmatic access to the GraphQL API.
    Tokens have scoped permissions and optional expiration."""

    typename__: str = Field(alias="__typename")
    id: str = Field(description="Unique token identifier.")
    "Unique token identifier."
    token_name: str = Field(
        alias="tokenName", description="Human-readable name for the token."
    )
    "Human-readable name for the token."
    token_value: Optional[str] = Field(
        alias="tokenValue",
        description="The secret token value (only returned on creation, null thereafter).",
    )
    "The secret token value (only returned on creation, null thereafter)."
    permissions: list[str] = Field(
        description="List of granted permissions (read:streams, write:streams, etc.)."
    )
    "List of granted permissions (read:streams, write:streams, etc.)."
    status: str = Field(description="Token status (active, revoked, expired).")
    "Token status (active, revoked, expired)."
    last_used_at: Optional[datetime] = Field(
        alias="lastUsedAt", description="When the token was last used for API access."
    )
    "When the token was last used for API access."
    expires_at: Optional[datetime] = Field(
        alias="expiresAt", description="When the token expires (null for non-expiring)."
    )
    "When the token expires (null for non-expiring)."
    created_at: Optional[datetime] = Field(
        alias="createdAt", description="When the token was created."
    )
    "When the token was created."


class EventArtifact(BaseModel):
    """Part of a public event payload (frameworks.events.public.v1.Artifact)."""

    artifact_id: str = Field(alias="artifactId")
    kind: EventArtifactKind
    stream_id: str = Field(alias="streamId")
    playback_id: str = Field(alias="playbackId")


class EventMoney(BaseModel):
    """Part of a public event payload (frameworks.events.public.v1.Money)."""

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
    platform: Optional[str] = Field(
        description="Platform identifier (twitch, youtube, facebook, kick, x, custom)."
    )
    "Platform identifier (twitch, youtube, facebook, kick, x, custom)."
    name: str = Field(description="User-friendly label for this target.")
    "User-friendly label for this target."
    target_uri: str = Field(
        alias="targetUri",
        description="Target URI (masked in responses — stream key portion is redacted).",
    )
    "Target URI (masked in responses — stream key portion is redacted)."
    is_enabled: bool = Field(
        alias="isEnabled",
        description="Whether this target is enabled for automatic push on stream start.",
    )
    "Whether this target is enabled for automatic push on stream start."
    status: str = Field(
        description="Current push status: pending, pushing, retrying, stopping, idle, or failed."
    )
    "Current push status: pending, pushing, retrying, stopping, idle, or failed."
    last_error: Optional[str] = Field(
        alias="lastError", description="Last error message if push failed."
    )
    "Last error message if push failed."
    reason_code: Optional[str] = Field(
        alias="reasonCode", description="Stable machine-readable lifecycle reason code."
    )
    "Stable machine-readable lifecycle reason code."
    last_pushed_at: Optional[datetime] = Field(
        alias="lastPushedAt", description="When this target last successfully pushed."
    )
    "When this target last successfully pushed."
    created_at: datetime = Field(alias="createdAt")


class RateLimitError(BaseModel):
    typename__: str = Field(alias="__typename")
    message: str
    code: Optional[str]
    retry_after: Optional[int] = Field(alias="retryAfter")


class SigningKey(BaseModel):
    """A customer-managed signing key for issuing viewer playback JWTs. The private
    key is returned exactly once at creation time (in CreateSigningKeySuccess);
    FrameWorks stores only the public key. Up to 10 active keys per tenant."""

    typename__: str = Field(alias="__typename")
    id: str
    kid: str = Field(
        description="Short ID embedded in JWT header (`kid`) for fast key lookup."
    )
    "Short ID embedded in JWT header (`kid`) for fast key lookup."
    name: str = Field(description="Customer-supplied label.")
    "Customer-supplied label."
    algorithm: SigningKeyAlgorithm = Field(description="Algorithm — ES256 only for v1.")
    "Algorithm — ES256 only for v1."
    public_key_pem: str = Field(
        alias="publicKeyPem",
        description="Public key in PEM format. The private key is never stored.",
    )
    "Public key in PEM format. The private key is never stored."
    status: SigningKeyStatus = Field(description="Lifecycle status.")
    "Lifecycle status."
    created_at: datetime = Field(alias="createdAt")
    last_used_at: Optional[datetime] = Field(
        alias="lastUsedAt",
        description="Last time a JWT signed by this key successfully verified at playback.",
    )
    "Last time a JWT signed by this key successfully verified at playback."
    revoked_at: Optional[datetime] = Field(
        alias="revokedAt", description="When the key was revoked (null if active)."
    )
    "When the key was revoked (null if active)."


class StorageArtifact(BaseModel):
    key: str
    kind: StorageArtifactKind
    id: str
    hash: str
    playback_id: Optional[str] = Field(alias="playbackId")
    stream_id: Optional[str] = Field(alias="streamId")
    stream_title: str = Field(alias="streamTitle")
    title: str
    description: Optional[str] = Field(
        description="User-provided description (VOD uploads and clips); null when unset."
    )
    "User-provided description (VOD uploads and clips); null when unset."
    error_message: Optional[str] = Field(
        alias="errorMessage",
        description="Processing failure detail for a failed artifact; null when not failed.",
    )
    "Processing failure detail for a failed artifact; null when not failed."
    size_bytes: Optional[float] = Field(alias="sizeBytes")
    status: str
    created_at: datetime = Field(alias="createdAt")
    updated_at: datetime = Field(alias="updatedAt")
    expires_at: Optional[datetime] = Field(alias="expiresAt")
    delete_id: str = Field(alias="deleteId")
    duration_seconds: Optional[float] = Field(
        alias="durationSeconds",
        description="Media duration in seconds. Clips retain their requested duration until measured; DVR/VOD duration may be null until finalized.",
    )
    "Media duration in seconds. Clips retain their requested duration until measured; DVR/VOD duration may be null until finalized."
    thumbnail_assets: Optional["StorageArtifactThumbnailAssets"] = Field(
        alias="thumbnailAssets",
        description="Poster + hover-scrub sprite assets, when the artifact has thumbnails.",
    )
    "Poster + hover-scrub sprite assets, when the artifact has thumbnails."


class StorageArtifactThumbnailAssets(ThumbnailAssets):
    """Chandler-served thumbnail asset URLs (poster, sprite, VTT cues). URL shape
    is `{chandlerBase}/assets/{assetKey}/poster.jpg`,
    `/assets/{assetKey}/sprite.jpg`, `/assets/{assetKey}/sprite.vtt` — Chandler
    serves the object key directly, no version resolution. assetKey is stream_id
    for live streams; clip_hash / dvr_hash / vod_hash (= artifact_hash) for artifacts."""

    pass


class Stream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

    typename__: str = Field(alias="__typename")
    id: str = Field(description="Global unique identifier for Relay compatibility.")
    "Global unique identifier for Relay compatibility."
    stream_id: str = Field(
        alias="streamId",
        description="Public stream UUID used for analytics and service APIs (not the Relay ID).",
    )
    "Public stream UUID used for analytics and service APIs (not the Relay ID)."
    name: str = Field(description="Human-readable display name for the stream.")
    "Human-readable display name for the stream."
    description: Optional[str] = Field(
        description="Optional description for the stream."
    )
    "Optional description for the stream."
    stream_key: Optional[str] = Field(
        alias="streamKey",
        description="Secret key for publisher-authenticated ingest; null for pull and managed sources.",
    )
    "Secret key for publisher-authenticated ingest; null for pull and managed sources."
    playback_id: str = Field(
        alias="playbackId", description="Public identifier for playback URLs."
    )
    "Public identifier for playback URLs."
    record: bool = Field(
        description="Whether DVR recording is enabled for this stream."
    )
    "Whether DVR recording is enabled for this stream."
    ingest_mode: IngestMode = Field(
        alias="ingestMode", description="How source media enters the stream."
    )
    "How source media enters the stream."
    pull_source: Optional["StreamPullSource"] = Field(
        alias="pullSource",
        description="Pull-source config for pull streams; null for push streams.",
    )
    "Pull-source config for pull streams; null for push streams."
    created_at: datetime = Field(
        alias="createdAt", description="When this stream was created."
    )
    "When this stream was created."
    updated_at: datetime = Field(
        alias="updatedAt", description="When this stream was last modified."
    )
    "When this stream was last modified."
    dvr_chapter_mode: Optional[DVRChapterMode] = Field(
        alias="dvrChapterMode",
        description="DVR chapter rotation mode. Snapshotted onto the DVR artifact at StartDVR;\nchanges take effect on the next recording. null/NONE = chapters disabled.",
    )
    "DVR chapter rotation mode. Snapshotted onto the DVR artifact at StartDVR;\nchanges take effect on the next recording. null/NONE = chapters disabled."
    dvr_chapter_interval_seconds: Optional[int] = Field(
        alias="dvrChapterIntervalSeconds",
        description="Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour).",
    )
    "Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour)."
    monitoring: MonitoringToggle = Field(
        description="Per-stream Skipper monitoring override (INHERIT follows tier)."
    )
    "Per-stream Skipper monitoring override (INHERIT follows tier)."
    playback_policy: Optional["StreamPlaybackPolicy"] = Field(
        alias="playbackPolicy",
        description="Playback access policy. null/PUBLIC = anyone with the playbackId can watch.",
    )
    "Playback access policy. null/PUBLIC = anyone with the playbackId can watch."
    metrics: Optional["StreamMetrics"] = Field(
        description="Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics."
    )
    "Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics."


class StreamPullSource(BaseModel):
    """Redacted pull-input source configuration."""

    source_uri_redacted: str = Field(
        alias="sourceUriRedacted",
        description="Redacted upstream URI with credentials removed.",
    )
    "Redacted upstream URI with credentials removed."
    enabled: bool = Field(
        description="Whether the media plane may pull from the source."
    )
    "Whether the media plane may pull from the source."
    class_: str = Field(
        alias="class", description="Eligibility class: public or private."
    )
    "Eligibility class: public or private."


class StreamPlaybackPolicy(PlaybackPolicy):
    """Per-playback-object access policy. Foghorn reads this in the USER_NEW
    trigger handler; the requiresAuth marker (on the playback object itself)
    gates whether the full policy is fetched at all."""

    pass


class StreamMetrics(BaseModel):
    """Real-time operational metrics for a stream from the analytics data plane.
    Updated frequently while stream is live, represents latest known state."""

    status: StreamStatus = Field(
        description="Current lifecycle status of the stream (OFFLINE, CONNECTING, LIVE, etc.)."
    )
    "Current lifecycle status of the stream (OFFLINE, CONNECTING, LIVE, etc.)."
    is_live: bool = Field(
        alias="isLive", description="Whether the stream is currently broadcasting."
    )
    "Whether the stream is currently broadcasting."
    current_viewers: int = Field(
        alias="currentViewers", description="Number of viewers currently watching."
    )
    "Number of viewers currently watching."
    started_at: Optional[datetime] = Field(
        alias="startedAt",
        description="When the current live session started (null if offline).",
    )
    "When the current live session started (null if offline)."
    updated_at: datetime = Field(
        alias="updatedAt", description="When these metrics were last updated."
    )
    "When these metrics were last updated."


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
    """A Video-on-Demand asset uploaded by the tenant.
    VOD assets can be played back using the playbackId in playback URLs."""

    typename__: str = Field(alias="__typename")
    id: str = Field(description="Global unique identifier for Relay compatibility.")
    "Global unique identifier for Relay compatibility."
    artifact_hash: str = Field(
        alias="artifactHash",
        description="Internal hash used for playback URL resolution.",
    )
    "Internal hash used for playback URL resolution."
    playback_id: str = Field(
        alias="playbackId",
        description="Public playback identifier for generating playback URLs.",
    )
    "Public playback identifier for generating playback URLs."
    stream_id: Optional[str] = Field(
        alias="streamId",
        description="Source stream UUID for stream-derived VOD artifacts such as DVR chapters.",
    )
    "Source stream UUID for stream-derived VOD artifacts such as DVR chapters."
    title: Optional[str] = Field(description="Optional display title for the asset.")
    "Optional display title for the asset."
    description: Optional[str] = Field(
        description="Optional description of the asset content."
    )
    "Optional description of the asset content."
    filename: Optional[str] = Field(description="Original filename when uploaded.")
    "Original filename when uploaded."
    status: VodAssetStatus = Field(
        description="Current processing/storage status of the asset."
    )
    "Current processing/storage status of the asset."
    size_bytes: Optional[float] = Field(
        alias="sizeBytes",
        description="File size in bytes (available after validation).",
    )
    "File size in bytes (available after validation)."
    duration_ms: Optional[int] = Field(
        alias="durationMs", description="Video duration in milliseconds."
    )
    "Video duration in milliseconds."
    resolution: Optional[str] = Field(
        description="Video resolution (e.g., '1920x1080')."
    )
    "Video resolution (e.g., '1920x1080')."
    video_codec: Optional[str] = Field(
        alias="videoCodec", description="Video codec (h264, h265, vp9, av1)."
    )
    "Video codec (h264, h265, vp9, av1)."
    audio_codec: Optional[str] = Field(
        alias="audioCodec", description="Audio codec (aac, opus)."
    )
    "Audio codec (aac, opus)."
    bitrate_kbps: Optional[int] = Field(
        alias="bitrateKbps", description="Average bitrate in kbps."
    )
    "Average bitrate in kbps."
    created_at: datetime = Field(
        alias="createdAt", description="When the asset was created/uploaded."
    )
    "When the asset was created/uploaded."
    updated_at: datetime = Field(
        alias="updatedAt", description="When the asset was last modified."
    )
    "When the asset was last modified."
    expires_at: Optional[datetime] = Field(
        alias="expiresAt", description="Optional expiration time for auto-deletion."
    )
    "Optional expiration time for auto-deletion."
    error_message: Optional[str] = Field(
        alias="errorMessage", description="Error message if processing failed."
    )
    "Error message if processing failed."
    playback_policy: Optional["VodAssetPlaybackPolicy"] = Field(
        alias="playbackPolicy",
        description="Playback access policy. null/PUBLIC = anyone with the playbackId can watch.",
    )
    "Playback access policy. null/PUBLIC = anyone with the playbackId can watch."
    thumbnail_assets: Optional["VodAssetThumbnailAssets"] = Field(
        alias="thumbnailAssets",
        description="Server-resolved Chandler URLs for the VOD's poster and sprite thumbnails.\nNull until Foghorn confirms the thumbnail upload.",
    )
    "Server-resolved Chandler URLs for the VOD's poster and sprite thumbnails.\nNull until Foghorn confirms the thumbnail upload."
    effective_retention: Optional["VodAssetEffectiveRetention"] = Field(
        alias="effectiveRetention",
        description="Resolved retention horizon with the source of the decision (per-asset\noverride → per-stream override → tenant default → tier entitlement).\nNull while the asset's retention_until column is unset (infinite).",
    )
    "Resolved retention horizon with the source of the decision (per-asset\noverride → per-stream override → tenant default → tier entitlement).\nNull while the asset's retention_until column is unset (infinite)."


class VodAssetPlaybackPolicy(PlaybackPolicy):
    """Per-playback-object access policy. Foghorn reads this in the USER_NEW
    trigger handler; the requiresAuth marker (on the playback object itself)
    gates whether the full policy is fetched at all."""

    pass


class VodAssetThumbnailAssets(ThumbnailAssets):
    """Chandler-served thumbnail asset URLs (poster, sprite, VTT cues). URL shape
    is `{chandlerBase}/assets/{assetKey}/poster.jpg`,
    `/assets/{assetKey}/sprite.jpg`, `/assets/{assetKey}/sprite.vtt` — Chandler
    serves the object key directly, no version resolution. assetKey is stream_id
    for live streams; clip_hash / dvr_hash / vod_hash (= artifact_hash) for artifacts."""

    pass


class VodAssetEffectiveRetention(EffectiveRetention):
    """Resolved retention horizon for a single asset (DVR, clip, or VOD).
    Embedded on Clip, DVRRequest, and VodAsset."""

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
