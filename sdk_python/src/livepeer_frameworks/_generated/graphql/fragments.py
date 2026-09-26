from datetime import datetime
from typing import Any, Optional

from pydantic import Field

from .base_model import BaseModel
from .enums import (
    BufferState,
    ClusterPricingModel,
    ClusterSubscriptionStatus,
    ClusterVisibility,
    ConversationStatus,
    CryptoAsset,
    DVRChapterMode,
    DVRChapterState,
    EventArtifactKind,
    IncidentEventKind,
    IncidentResolution,
    IncidentScope,
    IncidentStatus,
    IngestEndpointKind,
    IngestMode,
    InstanceStatus,
    InvoiceStatus,
    MediaPlacementClass,
    MediaPlacementErrorCode,
    MediaPlacementOptionKind,
    MediaPlacementRolloutStatus,
    MediaPlacementScopeKind,
    MediaPlacementVerb,
    MediaPlacementWarningSeverity,
    MessageSender,
    MonitoringToggle,
    NodeOperationalMode,
    NodeStatus,
    PaymentMethod,
    PaymentStatus,
    PlaybackPolicyType,
    RetentionSource,
    SigningKeyAlgorithm,
    SigningKeyStatus,
    SourceLocationMode,
    StorageArtifactKind,
    StreamEventSource,
    StreamEventType,
    StreamStatus,
    ValidationStatus,
    VodAssetStatus,
    WebhookDeliveryKind,
    WebhookDeliveryStatus,
    WebhookEndpointDisabledReason,
    WebhookEndpointStatus,
)


class APIUsageRecordDefault(BaseModel):
    id: str
    timestamp: datetime
    auth_type: str = Field(alias="authType")
    operation_type: str = Field(alias="operationType")
    operation_name: str = Field(alias="operationName")
    request_count: int = Field(alias="requestCount")
    error_count: int = Field(alias="errorCount")
    total_duration_ms: float = Field(alias="totalDurationMs")
    total_complexity: int = Field(alias="totalComplexity")
    unique_users: int = Field(alias="uniqueUsers")
    unique_tokens: int = Field(alias="uniqueTokens")


class ArtifactEventDefault(BaseModel):
    id: str
    timestamp: datetime
    stream_id: str = Field(alias="streamId")
    stream: Optional["ArtifactEventDefaultStream"]
    playback_id: Optional[str] = Field(alias="playbackId")
    stage: str
    content_type: Optional[str] = Field(alias="contentType")
    start_unix: Optional[int] = Field(alias="startUnix")
    stop_unix: Optional[int] = Field(alias="stopUnix")
    ingest_node_id: Optional[str] = Field(alias="ingestNodeId")
    percent: Optional[int]
    message: Optional[str]
    file_path: Optional[str] = Field(alias="filePath")
    s_3_url: Optional[str] = Field(alias="s3Url")
    size_bytes: Optional[float] = Field(alias="sizeBytes")
    expires_at: Optional[int] = Field(alias="expiresAt")


class ArtifactEventDefaultStream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

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
        description="Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path.",
    )
    "Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path."
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
    pull_source: Optional["ArtifactEventDefaultStreamPullSource"] = Field(
        alias="pullSource",
        description="Pull-source config for pull streams; null for push streams.",
    )
    "Pull-source config for pull streams; null for push streams."
    managed_source: Optional["ArtifactEventDefaultStreamManagedSource"] = Field(
        alias="managedSource",
        description="Safe source summary for managed streams; null for push and pull streams.",
    )
    "Safe source summary for managed streams; null for push and pull streams."
    source_location: "ArtifactEventDefaultStreamSourceLocation" = Field(
        alias="sourceLocation",
        description="Where the stream's source may be ingested, derived from the stream's own ingest placement rules.",
    )
    "Where the stream's source may be ingested, derived from the stream's own ingest placement rules."
    created_at: datetime = Field(
        alias="createdAt", description="When this stream was created."
    )
    "When this stream was created."
    updated_at: datetime = Field(
        alias="updatedAt", description="When this stream was last modified."
    )
    "When this stream was last modified."
    metrics: Optional["ArtifactEventDefaultStreamMetrics"] = Field(
        description="Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    )
    "Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    push_targets: list["ArtifactEventDefaultStreamPushTargets"] = Field(
        alias="pushTargets",
        description="Configured multistream push targets for this stream.",
    )
    "Configured multistream push targets for this stream."
    playback_policy: Optional["ArtifactEventDefaultStreamPlaybackPolicy"] = Field(
        alias="playbackPolicy",
        description="Playback access policy. null/PUBLIC = anyone with the playbackId can watch.",
    )
    "Playback access policy. null/PUBLIC = anyone with the playbackId can watch."
    dvr_chapter_mode: Optional[DVRChapterMode] = Field(
        alias="dvrChapterMode",
        description="How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast.",
    )
    "How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast."
    dvr_chapter_interval_seconds: Optional[int] = Field(
        alias="dvrChapterIntervalSeconds",
        description="Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour).",
    )
    "Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour)."
    monitoring: MonitoringToggle = Field(
        description="Per-stream Skipper monitoring override (INHERIT follows tier)."
    )
    "Per-stream Skipper monitoring override (INHERIT follows tier)."
    thumbnail_assets: Optional["ArtifactEventDefaultStreamThumbnailAssets"] = Field(
        alias="thumbnailAssets",
        description="Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles.",
    )
    "Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles."
    retention_overrides: Optional["ArtifactEventDefaultStreamRetentionOverrides"] = (
        Field(
            alias="retentionOverrides",
            description="Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here.",
        )
    )
    "Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here."


class ArtifactEventDefaultStreamPullSource(BaseModel):
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


class ArtifactEventDefaultStreamManagedSource(BaseModel):
    """Safe summary of an operator-managed source. Literal paths, commands, and
    credentials are intentionally not exposed through the tenant API."""

    source_kind: str = Field(
        alias="sourceKind", description="Managed source form: file, playlist, or exec."
    )
    "Managed source form: file, playlist, or exec."
    always_on: bool = Field(
        alias="alwaysOn",
        description="Whether the source is kept active without waiting for a viewer.",
    )
    "Whether the source is kept active without waiting for a viewer."
    placement_count: int = Field(
        alias="placementCount",
        description="Number of source placements requested by the operator configuration.",
    )
    "Number of source placements requested by the operator configuration."


class ArtifactEventDefaultStreamSourceLocation(BaseModel):
    mode: SourceLocationMode
    avoid_node_ids: list[str] = Field(
        alias="avoidNodeIds", description="Empty unless mode is RESTRICTED."
    )
    "Empty unless mode is RESTRICTED."


class ArtifactEventDefaultStreamMetrics(BaseModel):
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
    node_id: Optional[str] = Field(
        alias="nodeId", description="ID of the edge node handling this stream."
    )
    "ID of the edge node handling this stream."
    track_count: Optional[int] = Field(
        alias="trackCount", description="Number of quality tracks available."
    )
    "Number of quality tracks available."
    total_inputs: Optional[int] = Field(
        alias="totalInputs", description="Total ingest connections to this stream."
    )
    "Total ingest connections to this stream."
    uploaded_bytes: float = Field(
        alias="uploadedBytes",
        description="Total bytes uploaded (ingested) for current session.",
    )
    "Total bytes uploaded (ingested) for current session."
    downloaded_bytes: float = Field(
        alias="downloadedBytes",
        description="Total bytes downloaded (egress) for current session.",
    )
    "Total bytes downloaded (egress) for current session."
    viewer_seconds: float = Field(
        alias="viewerSeconds", description="Total viewer-seconds accumulated."
    )
    "Total viewer-seconds accumulated."
    packets_sent: Optional[float] = Field(
        alias="packetsSent", description="Total packets sent to viewers."
    )
    "Total packets sent to viewers."
    packets_lost: Optional[float] = Field(
        alias="packetsLost", description="Total packets lost in transit."
    )
    "Total packets lost in transit."
    packets_retransmitted: Optional[float] = Field(
        alias="packetsRetransmitted", description="Total packets retransmitted."
    )
    "Total packets retransmitted."
    buffer_state: Optional[str] = Field(
        alias="bufferState",
        description="Buffer health state (HEALTHY, WARNING, CRITICAL).",
    )
    "Buffer health state (HEALTHY, WARNING, CRITICAL)."
    quality_tier: Optional[str] = Field(
        alias="qualityTier",
        description="Highest quality tier available (4K, 1080p, 720p, etc.).",
    )
    "Highest quality tier available (4K, 1080p, 720p, etc.)."
    primary_width: Optional[int] = Field(
        alias="primaryWidth", description="Primary video track width in pixels."
    )
    "Primary video track width in pixels."
    primary_height: Optional[int] = Field(
        alias="primaryHeight", description="Primary video track height in pixels."
    )
    "Primary video track height in pixels."
    primary_fps: Optional[float] = Field(
        alias="primaryFps", description="Primary video track framerate."
    )
    "Primary video track framerate."
    primary_codec: Optional[str] = Field(
        alias="primaryCodec",
        description="Primary video codec (H.264, H.265, VP9, AV1).",
    )
    "Primary video codec (H.264, H.265, VP9, AV1)."
    primary_bitrate: Optional[int] = Field(
        alias="primaryBitrate", description="Primary video bitrate in kbps."
    )
    "Primary video bitrate in kbps."
    has_issues: Optional[bool] = Field(
        alias="hasIssues", description="Whether the stream has active quality issues."
    )
    "Whether the stream has active quality issues."
    issues_description: Optional[str] = Field(
        alias="issuesDescription",
        description="Human-readable description of current issues.",
    )
    "Human-readable description of current issues."


class ArtifactEventDefaultStreamPushTargets(BaseModel):
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


class ArtifactEventDefaultStreamPlaybackPolicy(BaseModel):
    """Per-playback-object access policy. Foghorn reads this in the USER_NEW
    trigger handler; the requiresAuth marker (on the playback object itself)
    gates whether the full policy is fetched at all."""

    type_: PlaybackPolicyType = Field(alias="type")
    allowed_origins: list[str] = Field(
        alias="allowedOrigins",
        description="Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied.",
    )
    "Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied."


class ArtifactEventDefaultStreamThumbnailAssets(BaseModel):
    """Chandler-served thumbnail asset URLs (poster, sprite, VTT cues). URL shape
    is `{chandlerBase}/assets/{assetKey}/poster.jpg`,
    `/assets/{assetKey}/sprite.jpg`, `/assets/{assetKey}/sprite.vtt` — Chandler
    serves the object key directly, no version resolution. assetKey is stream_id
    for live streams; clip_hash / dvr_hash / vod_hash (= artifact_hash) for artifacts."""

    poster_url: str = Field(alias="posterUrl")
    sprite_vtt_url: str = Field(alias="spriteVttUrl")
    sprite_jpg_url: str = Field(alias="spriteJpgUrl")
    asset_key: str = Field(alias="assetKey")


class ArtifactEventDefaultStreamRetentionOverrides(BaseModel):
    """Per-stream retention overrides for DVR and clips. VOD uploads aren't
    stream-bound, so they have no stream-level knob here."""

    stream_id: str = Field(alias="streamId")
    dvr_retention_days_override: Optional[int] = Field(
        alias="dvrRetentionDaysOverride",
        description="Null = no override (inherit tenant default). 0 = no auto-expire.",
    )
    "Null = no override (inherit tenant default). 0 = no auto-expire."
    clip_retention_days_override: Optional[int] = Field(
        alias="clipRetentionDaysOverride"
    )


class ArtifactEventInNodeDefault(BaseModel):
    id: str
    timestamp: datetime
    stream_id: str = Field(alias="streamId")
    stream: Optional["ArtifactEventInNodeDefaultStream"]
    artifact_event_playback_id: Optional[str] = Field(alias="artifactEventPlaybackId")
    stage: str
    content_type: Optional[str] = Field(alias="contentType")
    start_unix: Optional[int] = Field(alias="startUnix")
    stop_unix: Optional[int] = Field(alias="stopUnix")
    ingest_node_id: Optional[str] = Field(alias="ingestNodeId")
    percent: Optional[int]
    message: Optional[str]
    file_path: Optional[str] = Field(alias="filePath")
    s_3_url: Optional[str] = Field(alias="s3Url")
    size_bytes: Optional[float] = Field(alias="sizeBytes")
    artifact_event_expires_at: Optional[int] = Field(alias="artifactEventExpiresAt")


class ArtifactEventInNodeDefaultStream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

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
        description="Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path.",
    )
    "Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path."
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
    pull_source: Optional["ArtifactEventInNodeDefaultStreamPullSource"] = Field(
        alias="pullSource",
        description="Pull-source config for pull streams; null for push streams.",
    )
    "Pull-source config for pull streams; null for push streams."
    managed_source: Optional["ArtifactEventInNodeDefaultStreamManagedSource"] = Field(
        alias="managedSource",
        description="Safe source summary for managed streams; null for push and pull streams.",
    )
    "Safe source summary for managed streams; null for push and pull streams."
    source_location: "ArtifactEventInNodeDefaultStreamSourceLocation" = Field(
        alias="sourceLocation",
        description="Where the stream's source may be ingested, derived from the stream's own ingest placement rules.",
    )
    "Where the stream's source may be ingested, derived from the stream's own ingest placement rules."
    created_at: datetime = Field(
        alias="createdAt", description="When this stream was created."
    )
    "When this stream was created."
    updated_at: datetime = Field(
        alias="updatedAt", description="When this stream was last modified."
    )
    "When this stream was last modified."
    metrics: Optional["ArtifactEventInNodeDefaultStreamMetrics"] = Field(
        description="Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    )
    "Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    push_targets: list["ArtifactEventInNodeDefaultStreamPushTargets"] = Field(
        alias="pushTargets",
        description="Configured multistream push targets for this stream.",
    )
    "Configured multistream push targets for this stream."
    playback_policy: Optional["ArtifactEventInNodeDefaultStreamPlaybackPolicy"] = Field(
        alias="playbackPolicy",
        description="Playback access policy. null/PUBLIC = anyone with the playbackId can watch.",
    )
    "Playback access policy. null/PUBLIC = anyone with the playbackId can watch."
    dvr_chapter_mode: Optional[DVRChapterMode] = Field(
        alias="dvrChapterMode",
        description="How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast.",
    )
    "How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast."
    dvr_chapter_interval_seconds: Optional[int] = Field(
        alias="dvrChapterIntervalSeconds",
        description="Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour).",
    )
    "Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour)."
    monitoring: MonitoringToggle = Field(
        description="Per-stream Skipper monitoring override (INHERIT follows tier)."
    )
    "Per-stream Skipper monitoring override (INHERIT follows tier)."
    thumbnail_assets: Optional["ArtifactEventInNodeDefaultStreamThumbnailAssets"] = (
        Field(
            alias="thumbnailAssets",
            description="Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles.",
        )
    )
    "Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles."
    retention_overrides: Optional[
        "ArtifactEventInNodeDefaultStreamRetentionOverrides"
    ] = Field(
        alias="retentionOverrides",
        description="Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here.",
    )
    "Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here."


class ArtifactEventInNodeDefaultStreamPullSource(BaseModel):
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


class ArtifactEventInNodeDefaultStreamManagedSource(BaseModel):
    """Safe summary of an operator-managed source. Literal paths, commands, and
    credentials are intentionally not exposed through the tenant API."""

    source_kind: str = Field(
        alias="sourceKind", description="Managed source form: file, playlist, or exec."
    )
    "Managed source form: file, playlist, or exec."
    always_on: bool = Field(
        alias="alwaysOn",
        description="Whether the source is kept active without waiting for a viewer.",
    )
    "Whether the source is kept active without waiting for a viewer."
    placement_count: int = Field(
        alias="placementCount",
        description="Number of source placements requested by the operator configuration.",
    )
    "Number of source placements requested by the operator configuration."


class ArtifactEventInNodeDefaultStreamSourceLocation(BaseModel):
    mode: SourceLocationMode
    avoid_node_ids: list[str] = Field(
        alias="avoidNodeIds", description="Empty unless mode is RESTRICTED."
    )
    "Empty unless mode is RESTRICTED."


class ArtifactEventInNodeDefaultStreamMetrics(BaseModel):
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
    node_id: Optional[str] = Field(
        alias="nodeId", description="ID of the edge node handling this stream."
    )
    "ID of the edge node handling this stream."
    track_count: Optional[int] = Field(
        alias="trackCount", description="Number of quality tracks available."
    )
    "Number of quality tracks available."
    total_inputs: Optional[int] = Field(
        alias="totalInputs", description="Total ingest connections to this stream."
    )
    "Total ingest connections to this stream."
    uploaded_bytes: float = Field(
        alias="uploadedBytes",
        description="Total bytes uploaded (ingested) for current session.",
    )
    "Total bytes uploaded (ingested) for current session."
    downloaded_bytes: float = Field(
        alias="downloadedBytes",
        description="Total bytes downloaded (egress) for current session.",
    )
    "Total bytes downloaded (egress) for current session."
    viewer_seconds: float = Field(
        alias="viewerSeconds", description="Total viewer-seconds accumulated."
    )
    "Total viewer-seconds accumulated."
    packets_sent: Optional[float] = Field(
        alias="packetsSent", description="Total packets sent to viewers."
    )
    "Total packets sent to viewers."
    packets_lost: Optional[float] = Field(
        alias="packetsLost", description="Total packets lost in transit."
    )
    "Total packets lost in transit."
    packets_retransmitted: Optional[float] = Field(
        alias="packetsRetransmitted", description="Total packets retransmitted."
    )
    "Total packets retransmitted."
    buffer_state: Optional[str] = Field(
        alias="bufferState",
        description="Buffer health state (HEALTHY, WARNING, CRITICAL).",
    )
    "Buffer health state (HEALTHY, WARNING, CRITICAL)."
    quality_tier: Optional[str] = Field(
        alias="qualityTier",
        description="Highest quality tier available (4K, 1080p, 720p, etc.).",
    )
    "Highest quality tier available (4K, 1080p, 720p, etc.)."
    primary_width: Optional[int] = Field(
        alias="primaryWidth", description="Primary video track width in pixels."
    )
    "Primary video track width in pixels."
    primary_height: Optional[int] = Field(
        alias="primaryHeight", description="Primary video track height in pixels."
    )
    "Primary video track height in pixels."
    primary_fps: Optional[float] = Field(
        alias="primaryFps", description="Primary video track framerate."
    )
    "Primary video track framerate."
    primary_codec: Optional[str] = Field(
        alias="primaryCodec",
        description="Primary video codec (H.264, H.265, VP9, AV1).",
    )
    "Primary video codec (H.264, H.265, VP9, AV1)."
    primary_bitrate: Optional[int] = Field(
        alias="primaryBitrate", description="Primary video bitrate in kbps."
    )
    "Primary video bitrate in kbps."
    has_issues: Optional[bool] = Field(
        alias="hasIssues", description="Whether the stream has active quality issues."
    )
    "Whether the stream has active quality issues."
    issues_description: Optional[str] = Field(
        alias="issuesDescription",
        description="Human-readable description of current issues.",
    )
    "Human-readable description of current issues."


class ArtifactEventInNodeDefaultStreamPushTargets(BaseModel):
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


class ArtifactEventInNodeDefaultStreamPlaybackPolicy(BaseModel):
    """Per-playback-object access policy. Foghorn reads this in the USER_NEW
    trigger handler; the requiresAuth marker (on the playback object itself)
    gates whether the full policy is fetched at all."""

    type_: PlaybackPolicyType = Field(alias="type")
    allowed_origins: list[str] = Field(
        alias="allowedOrigins",
        description="Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied.",
    )
    "Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied."


class ArtifactEventInNodeDefaultStreamThumbnailAssets(BaseModel):
    """Chandler-served thumbnail asset URLs (poster, sprite, VTT cues). URL shape
    is `{chandlerBase}/assets/{assetKey}/poster.jpg`,
    `/assets/{assetKey}/sprite.jpg`, `/assets/{assetKey}/sprite.vtt` — Chandler
    serves the object key directly, no version resolution. assetKey is stream_id
    for live streams; clip_hash / dvr_hash / vod_hash (= artifact_hash) for artifacts."""

    poster_url: str = Field(alias="posterUrl")
    sprite_vtt_url: str = Field(alias="spriteVttUrl")
    sprite_jpg_url: str = Field(alias="spriteJpgUrl")
    asset_key: str = Field(alias="assetKey")


class ArtifactEventInNodeDefaultStreamRetentionOverrides(BaseModel):
    """Per-stream retention overrides for DVR and clips. VOD uploads aren't
    stream-bound, so they have no stream-level knob here."""

    stream_id: str = Field(alias="streamId")
    dvr_retention_days_override: Optional[int] = Field(
        alias="dvrRetentionDaysOverride",
        description="Null = no override (inherit tenant default). 0 = no auto-expire.",
    )
    "Null = no override (inherit tenant default). 0 = no auto-expire."
    clip_retention_days_override: Optional[int] = Field(
        alias="clipRetentionDaysOverride"
    )


class ArtifactStateDefault(BaseModel):
    stream_id: str = Field(alias="streamId")
    stream: Optional["ArtifactStateDefaultStream"]
    playback_id: Optional[str] = Field(alias="playbackId")
    content_type: str = Field(alias="contentType")
    stage: str
    progress_percent: int = Field(alias="progressPercent")
    error_message: Optional[str] = Field(alias="errorMessage")
    requested_at: datetime = Field(alias="requestedAt")
    started_at: Optional[datetime] = Field(alias="startedAt")
    completed_at: Optional[datetime] = Field(alias="completedAt")
    clip_start_unix: Optional[int] = Field(alias="clipStartUnix")
    clip_stop_unix: Optional[int] = Field(alias="clipStopUnix")
    segment_count: Optional[int] = Field(alias="segmentCount")
    manifest_path: Optional[str] = Field(alias="manifestPath")
    file_path: Optional[str] = Field(alias="filePath")
    s_3_url: Optional[str] = Field(alias="s3Url")
    size_bytes: Optional[float] = Field(alias="sizeBytes")
    processing_node_id: Optional[str] = Field(alias="processingNodeId")
    expires_at: Optional[datetime] = Field(alias="expiresAt")


class ArtifactStateDefaultStream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

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
        description="Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path.",
    )
    "Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path."
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
    pull_source: Optional["ArtifactStateDefaultStreamPullSource"] = Field(
        alias="pullSource",
        description="Pull-source config for pull streams; null for push streams.",
    )
    "Pull-source config for pull streams; null for push streams."
    managed_source: Optional["ArtifactStateDefaultStreamManagedSource"] = Field(
        alias="managedSource",
        description="Safe source summary for managed streams; null for push and pull streams.",
    )
    "Safe source summary for managed streams; null for push and pull streams."
    source_location: "ArtifactStateDefaultStreamSourceLocation" = Field(
        alias="sourceLocation",
        description="Where the stream's source may be ingested, derived from the stream's own ingest placement rules.",
    )
    "Where the stream's source may be ingested, derived from the stream's own ingest placement rules."
    created_at: datetime = Field(
        alias="createdAt", description="When this stream was created."
    )
    "When this stream was created."
    updated_at: datetime = Field(
        alias="updatedAt", description="When this stream was last modified."
    )
    "When this stream was last modified."
    metrics: Optional["ArtifactStateDefaultStreamMetrics"] = Field(
        description="Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    )
    "Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    push_targets: list["ArtifactStateDefaultStreamPushTargets"] = Field(
        alias="pushTargets",
        description="Configured multistream push targets for this stream.",
    )
    "Configured multistream push targets for this stream."
    playback_policy: Optional["ArtifactStateDefaultStreamPlaybackPolicy"] = Field(
        alias="playbackPolicy",
        description="Playback access policy. null/PUBLIC = anyone with the playbackId can watch.",
    )
    "Playback access policy. null/PUBLIC = anyone with the playbackId can watch."
    dvr_chapter_mode: Optional[DVRChapterMode] = Field(
        alias="dvrChapterMode",
        description="How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast.",
    )
    "How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast."
    dvr_chapter_interval_seconds: Optional[int] = Field(
        alias="dvrChapterIntervalSeconds",
        description="Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour).",
    )
    "Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour)."
    monitoring: MonitoringToggle = Field(
        description="Per-stream Skipper monitoring override (INHERIT follows tier)."
    )
    "Per-stream Skipper monitoring override (INHERIT follows tier)."
    thumbnail_assets: Optional["ArtifactStateDefaultStreamThumbnailAssets"] = Field(
        alias="thumbnailAssets",
        description="Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles.",
    )
    "Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles."
    retention_overrides: Optional["ArtifactStateDefaultStreamRetentionOverrides"] = (
        Field(
            alias="retentionOverrides",
            description="Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here.",
        )
    )
    "Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here."


class ArtifactStateDefaultStreamPullSource(BaseModel):
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


class ArtifactStateDefaultStreamManagedSource(BaseModel):
    """Safe summary of an operator-managed source. Literal paths, commands, and
    credentials are intentionally not exposed through the tenant API."""

    source_kind: str = Field(
        alias="sourceKind", description="Managed source form: file, playlist, or exec."
    )
    "Managed source form: file, playlist, or exec."
    always_on: bool = Field(
        alias="alwaysOn",
        description="Whether the source is kept active without waiting for a viewer.",
    )
    "Whether the source is kept active without waiting for a viewer."
    placement_count: int = Field(
        alias="placementCount",
        description="Number of source placements requested by the operator configuration.",
    )
    "Number of source placements requested by the operator configuration."


class ArtifactStateDefaultStreamSourceLocation(BaseModel):
    mode: SourceLocationMode
    avoid_node_ids: list[str] = Field(
        alias="avoidNodeIds", description="Empty unless mode is RESTRICTED."
    )
    "Empty unless mode is RESTRICTED."


class ArtifactStateDefaultStreamMetrics(BaseModel):
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
    node_id: Optional[str] = Field(
        alias="nodeId", description="ID of the edge node handling this stream."
    )
    "ID of the edge node handling this stream."
    track_count: Optional[int] = Field(
        alias="trackCount", description="Number of quality tracks available."
    )
    "Number of quality tracks available."
    total_inputs: Optional[int] = Field(
        alias="totalInputs", description="Total ingest connections to this stream."
    )
    "Total ingest connections to this stream."
    uploaded_bytes: float = Field(
        alias="uploadedBytes",
        description="Total bytes uploaded (ingested) for current session.",
    )
    "Total bytes uploaded (ingested) for current session."
    downloaded_bytes: float = Field(
        alias="downloadedBytes",
        description="Total bytes downloaded (egress) for current session.",
    )
    "Total bytes downloaded (egress) for current session."
    viewer_seconds: float = Field(
        alias="viewerSeconds", description="Total viewer-seconds accumulated."
    )
    "Total viewer-seconds accumulated."
    packets_sent: Optional[float] = Field(
        alias="packetsSent", description="Total packets sent to viewers."
    )
    "Total packets sent to viewers."
    packets_lost: Optional[float] = Field(
        alias="packetsLost", description="Total packets lost in transit."
    )
    "Total packets lost in transit."
    packets_retransmitted: Optional[float] = Field(
        alias="packetsRetransmitted", description="Total packets retransmitted."
    )
    "Total packets retransmitted."
    buffer_state: Optional[str] = Field(
        alias="bufferState",
        description="Buffer health state (HEALTHY, WARNING, CRITICAL).",
    )
    "Buffer health state (HEALTHY, WARNING, CRITICAL)."
    quality_tier: Optional[str] = Field(
        alias="qualityTier",
        description="Highest quality tier available (4K, 1080p, 720p, etc.).",
    )
    "Highest quality tier available (4K, 1080p, 720p, etc.)."
    primary_width: Optional[int] = Field(
        alias="primaryWidth", description="Primary video track width in pixels."
    )
    "Primary video track width in pixels."
    primary_height: Optional[int] = Field(
        alias="primaryHeight", description="Primary video track height in pixels."
    )
    "Primary video track height in pixels."
    primary_fps: Optional[float] = Field(
        alias="primaryFps", description="Primary video track framerate."
    )
    "Primary video track framerate."
    primary_codec: Optional[str] = Field(
        alias="primaryCodec",
        description="Primary video codec (H.264, H.265, VP9, AV1).",
    )
    "Primary video codec (H.264, H.265, VP9, AV1)."
    primary_bitrate: Optional[int] = Field(
        alias="primaryBitrate", description="Primary video bitrate in kbps."
    )
    "Primary video bitrate in kbps."
    has_issues: Optional[bool] = Field(
        alias="hasIssues", description="Whether the stream has active quality issues."
    )
    "Whether the stream has active quality issues."
    issues_description: Optional[str] = Field(
        alias="issuesDescription",
        description="Human-readable description of current issues.",
    )
    "Human-readable description of current issues."


class ArtifactStateDefaultStreamPushTargets(BaseModel):
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


class ArtifactStateDefaultStreamPlaybackPolicy(BaseModel):
    """Per-playback-object access policy. Foghorn reads this in the USER_NEW
    trigger handler; the requiresAuth marker (on the playback object itself)
    gates whether the full policy is fetched at all."""

    type_: PlaybackPolicyType = Field(alias="type")
    allowed_origins: list[str] = Field(
        alias="allowedOrigins",
        description="Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied.",
    )
    "Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied."


class ArtifactStateDefaultStreamThumbnailAssets(BaseModel):
    """Chandler-served thumbnail asset URLs (poster, sprite, VTT cues). URL shape
    is `{chandlerBase}/assets/{assetKey}/poster.jpg`,
    `/assets/{assetKey}/sprite.jpg`, `/assets/{assetKey}/sprite.vtt` — Chandler
    serves the object key directly, no version resolution. assetKey is stream_id
    for live streams; clip_hash / dvr_hash / vod_hash (= artifact_hash) for artifacts."""

    poster_url: str = Field(alias="posterUrl")
    sprite_vtt_url: str = Field(alias="spriteVttUrl")
    sprite_jpg_url: str = Field(alias="spriteJpgUrl")
    asset_key: str = Field(alias="assetKey")


class ArtifactStateDefaultStreamRetentionOverrides(BaseModel):
    """Per-stream retention overrides for DVR and clips. VOD uploads aren't
    stream-bound, so they have no stream-level knob here."""

    stream_id: str = Field(alias="streamId")
    dvr_retention_days_override: Optional[int] = Field(
        alias="dvrRetentionDaysOverride",
        description="Null = no override (inherit tenant default). 0 = no auto-expire.",
    )
    "Null = no override (inherit tenant default). 0 = no auto-expire."
    clip_retention_days_override: Optional[int] = Field(
        alias="clipRetentionDaysOverride"
    )


class AssetNodeCopiesDefault(BaseModel):
    """A node-copy listing plus whether it was capped at the per-request limit. When
    `truncated` is true, `copies` is not exhaustive — present the count as a lower bound
    (e.g. "500+") or a "results truncated" note, never as an exact total."""

    copies: list["AssetNodeCopiesDefaultCopies"]
    truncated: bool


class AssetNodeCopiesDefaultCopies(BaseModel):
    """One node's current local copy of an artifact. `role` is `origin` (a producer node's
    copy) or `cache` (a synced copy on another node)."""

    node_id: str = Field(alias="nodeId")
    node_name: Optional[str] = Field(alias="nodeName")
    cluster_id: Optional[str] = Field(alias="clusterId")
    region: Optional[str]
    latitude: Optional[float]
    longitude: Optional[float]
    role: str
    is_complete: bool = Field(alias="isComplete")
    size_bytes: Optional[float] = Field(alias="sizeBytes")
    updated_at: Optional[datetime] = Field(alias="updatedAt")


class AuthErrorDefault(BaseModel):
    message: str
    code: Optional[str]


class AuthError(BaseModel):
    typename__: str = Field(alias="__typename")
    message: str
    code: Optional[str]


class AvailableClusterDefault(BaseModel):
    cluster_id: str = Field(alias="clusterId")
    cluster_name: str = Field(alias="clusterName")
    tiers: list[str]
    auto_enroll: bool = Field(alias="autoEnroll")


class BalanceTransactionDefault(BaseModel):
    """A single balance transaction (top-up, usage deduction, refund, etc.)."""

    id: str = Field(description="Unique transaction identifier.")
    "Unique transaction identifier."
    tenant_id: str = Field(alias="tenantId", description="Owning tenant identifier.")
    "Owning tenant identifier."
    amount_cents: int = Field(
        alias="amountCents",
        description="Amount in cents (positive = credit, negative = debit).",
    )
    "Amount in cents (positive = credit, negative = debit)."
    balance_after_cents: int = Field(
        alias="balanceAfterCents", description="Balance after this transaction."
    )
    "Balance after this transaction."
    transaction_type: str = Field(
        alias="transactionType",
        description="Transaction type: topup, usage, refund, adjustment.",
    )
    "Transaction type: topup, usage, refund, adjustment."
    description: Optional[str] = Field(description="Human-readable description.")
    "Human-readable description."
    reference_id: Optional[str] = Field(
        alias="referenceId",
        description="Reference to related object (crypto payment, usage record, etc.).",
    )
    "Reference to related object (crypto payment, usage record, etc.)."
    reference_type: Optional[str] = Field(
        alias="referenceType", description="Type of the referenced object."
    )
    "Type of the referenced object."
    created_at: datetime = Field(
        alias="createdAt", description="When the transaction occurred."
    )
    "When the transaction occurred."


class BillingDetailsDefault(BaseModel):
    """Billing details for a tenant. These are optional for Free and small simplified
    top-ups, and required when paid postpaid collection or a full invoice needs them."""

    email: Optional[str] = Field(description="Billing contact email.")
    "Billing contact email."
    name: Optional[str] = Field(
        description="Customer legal or person name shown on full invoices."
    )
    "Customer legal or person name shown on full invoices."
    company: Optional[str] = Field(description="Company name for invoices.")
    "Company name for invoices."
    vat_number: Optional[str] = Field(
        alias="vatNumber", description="VAT number (EU format: XX123456789)."
    )
    "VAT number (EU format: XX123456789)."
    address: Optional["BillingDetailsDefaultAddress"] = Field(
        description="Structured billing address."
    )
    "Structured billing address."
    is_complete: bool = Field(
        alias="isComplete",
        description="True if all full-invoice fields are present (legal name + email + address).",
    )
    "True if all full-invoice fields are present (legal name + email + address)."
    updated_at: Optional[datetime] = Field(
        alias="updatedAt", description="When billing details were last updated."
    )
    "When billing details were last updated."
    presentment_currency: Optional[Any] = Field(
        alias="presentmentCurrency",
        description="Currency the tenant is invoiced and charged in (EUR, USD, GBP), derived from the billing address country. It cannot change while a provider subscription, an open invoice, or a card first payment started in the last day exists.",
    )
    "Currency the tenant is invoiced and charged in (EUR, USD, GBP), derived from the billing address country. It cannot change while a provider subscription, an open invoice, or a card first payment started in the last day exists."


class BillingDetailsDefaultAddress(BaseModel):
    """Structured billing address for invoices."""

    street: str = Field(description="Street address line 1.")
    "Street address line 1."
    city: str = Field(description="City name.")
    "City name."
    state: Optional[str] = Field(description="State or province.")
    "State or province."
    postal_code: str = Field(alias="postalCode", description="Postal or ZIP code.")
    "Postal or ZIP code."
    country: str = Field(description="ISO 3166-1 alpha-2 country code.")
    "ISO 3166-1 alpha-2 country code."


class BillingStatusDefault(BaseModel):
    current_tier: Optional["BillingStatusDefaultCurrentTier"] = Field(
        alias="currentTier"
    )
    subscription: Optional["BillingStatusDefaultSubscription"]
    billing_status: str = Field(alias="billingStatus")
    payment_methods: list[PaymentMethod] = Field(
        alias="paymentMethods",
        description="Invoice payment methods currently configured for this tenant.",
    )
    "Invoice payment methods currently configured for this tenant."
    collection_ready: bool = Field(
        alias="collectionReady",
        description="True only after a reusable postpaid provider subscription or mandate is confirmed.",
    )
    "True only after a reusable postpaid provider subscription or mandate is confirmed."
    collection_provider: Optional[str] = Field(
        alias="collectionProvider",
        description="Confirmed postpaid collection provider, when ready.",
    )
    "Confirmed postpaid collection provider, when ready."
    setup_providers: list[str] = Field(
        alias="setupProviders",
        description="Fully configured providers that can start postpaid setup.",
    )
    "Fully configured providers that can start postpaid setup."
    recent_payments: list["BillingStatusDefaultRecentPayments"] = Field(
        alias="recentPayments",
        description="Recent invoice payments, newest first. Retry or resume through createPayment on the invoice.",
    )
    "Recent invoice payments, newest first. Retry or resume through createPayment on the invoice."
    next_billing_date: Optional[datetime] = Field(alias="nextBillingDate")
    trial_ends_at: Optional[datetime] = Field(alias="trialEndsAt")
    outstanding_amount: float = Field(alias="outstandingAmount")
    currency: str
    live_usage: "BillingStatusDefaultLiveUsage" = Field(alias="liveUsage")
    invoice_preview: Optional["BillingStatusDefaultInvoicePreview"] = Field(
        alias="invoicePreview"
    )


class BillingStatusDefaultCurrentTier(BaseModel):
    """A subscription tier defining pricing rules, entitlements, and features.
    Tenants subscribe to a tier which determines their billing and capabilities."""

    id: str = Field(description="Unique tier identifier.")
    "Unique tier identifier."
    tier_name: str = Field(
        alias="tierName",
        description="Internal tier name (payg, free, supporter, developer, production, enterprise).",
    )
    "Internal tier name (payg, free, supporter, developer, production, enterprise)."
    tier_level: int = Field(
        alias="tierLevel",
        description="Numeric ordering of tiers; 0 = payg (prepaid), 1 = free, 5 = enterprise. Used to compare upgrade vs downgrade direction.",
    )
    "Numeric ordering of tiers; 0 = payg (prepaid), 1 = free, 5 = enterprise. Used to compare upgrade vs downgrade direction."
    display_name: str = Field(alias="displayName", description="User-facing tier name.")
    "User-facing tier name."
    description: Optional[str] = Field(description="Tier description for marketing.")
    "Tier description for marketing."
    base_price: float = Field(alias="basePrice", description="Base monthly price.")
    "Base monthly price."
    currency: str = Field(description="Currency code (USD, EUR, etc.).")
    "Currency code (USD, EUR, etc.)."
    billing_period: str = Field(
        alias="billingPeriod", description="Billing period (monthly, yearly)."
    )
    "Billing period (monthly, yearly)."
    features: "BillingStatusDefaultCurrentTierFeatures" = Field(
        description="Feature flags for this tier."
    )
    "Feature flags for this tier."
    pricing_rules: list["BillingStatusDefaultCurrentTierPricingRules"] = Field(
        alias="pricingRules",
        description="Priced behaviors per meter (rating engine input).",
    )
    "Priced behaviors per meter (rating engine input)."
    entitlements: list["BillingStatusDefaultCurrentTierEntitlements"] = Field(
        description="Non-billing grants (e.g. recording_retention_days). Values are JSON-encoded strings."
    )
    "Non-billing grants (e.g. recording_retention_days). Values are JSON-encoded strings."
    support_level: Optional[str] = Field(
        alias="supportLevel",
        description="Support level included (community, email, priority, dedicated).",
    )
    "Support level included (community, email, priority, dedicated)."
    sla_level: Optional[str] = Field(
        alias="slaLevel", description="SLA guarantee level for enterprise tiers."
    )
    "SLA guarantee level for enterprise tiers."
    metering_enabled: bool = Field(
        alias="meteringEnabled",
        description="Whether usage metering is enabled for this tier.",
    )
    "Whether usage metering is enabled for this tier."
    is_enterprise: bool = Field(
        alias="isEnterprise",
        description="Whether this is an enterprise tier with custom terms.",
    )
    "Whether this is an enterprise tier with custom terms."


class BillingStatusDefaultCurrentTierFeatures(BaseModel):
    """Contractual and enforced properties of a billing tier. Usage limits are
    entitlements, not features."""

    support_level: str = Field(
        alias="supportLevel",
        description="Support level (community, email, priority, dedicated).",
    )
    "Support level (community, email, priority, dedicated)."
    sla: bool = Field(description="Whether SLA guarantees apply.")
    "Whether SLA guarantees apply."
    processing_customizable: bool = Field(
        alias="processingCustomizable",
        description="Whether the tenant may override the tier's processing profiles.",
    )
    "Whether the tenant may override the tier's processing profiles."


class BillingStatusDefaultCurrentTierPricingRules(BaseModel):
    """A pricing rule: how a meter rates to money. Decimal fields are strings to preserve precision."""

    meter: str = Field(
        description="Canonical meter name (for example delivered_minutes, storage_gb_seconds_cold, media_seconds, or a marketplace-defined meter)."
    )
    "Canonical meter name (for example delivered_minutes, storage_gb_seconds_cold, media_seconds, or a marketplace-defined meter)."
    model: str = Field(
        description="Pricing model (tiered_graduated, all_usage, dimensioned)."
    )
    "Pricing model (tiered_graduated, all_usage, dimensioned)."
    currency: str = Field(description="Currency (ISO 4217).")
    "Currency (ISO 4217)."
    included_quantity: str = Field(
        alias="includedQuantity",
        description="Free quantity included before billing kicks in.",
    )
    "Free quantity included before billing kicks in."
    unit_price: str = Field(
        alias="unitPrice", description="Price per unit (decimal as string)."
    )
    "Price per unit (decimal as string)."
    config_json: str = Field(
        alias="configJson",
        description="Model-specific config (for example dimension selector rates); JSON-encoded.",
    )
    "Model-specific config (for example dimension selector rates); JSON-encoded."


class BillingStatusDefaultCurrentTierEntitlements(BaseModel):
    """A key-value entitlement entry. Values are JSON-encoded for type flexibility."""

    key: str
    value: str


class BillingStatusDefaultSubscription(BaseModel):
    id: str
    tenant_id: str = Field(alias="tenantId")
    tier_id: str = Field(alias="tierId")
    status: str
    billing_email: str = Field(alias="billingEmail")
    started_at: datetime = Field(alias="startedAt")
    trial_ends_at: Optional[datetime] = Field(alias="trialEndsAt")
    next_billing_date: Optional[datetime] = Field(alias="nextBillingDate")
    cancelled_at: Optional[datetime] = Field(alias="cancelledAt")
    custom_features: Optional["BillingStatusDefaultSubscriptionCustomFeatures"] = Field(
        alias="customFeatures"
    )
    pricing_overrides: list["BillingStatusDefaultSubscriptionPricingOverrides"] = Field(
        alias="pricingOverrides"
    )
    entitlement_overrides: list[
        "BillingStatusDefaultSubscriptionEntitlementOverrides"
    ] = Field(alias="entitlementOverrides")
    payment_method: Optional[str] = Field(alias="paymentMethod")
    billing_model: str = Field(alias="billingModel")
    pending_tier: Optional["BillingStatusDefaultSubscriptionPendingTier"] = Field(
        alias="pendingTier"
    )
    pending_effective_at: Optional[datetime] = Field(alias="pendingEffectiveAt")
    pending_reason: Optional[str] = Field(alias="pendingReason")
    presentment_currency: Any = Field(
        alias="presentmentCurrency",
        description="Currency the tenant is invoiced and charged in (EUR, USD, GBP), derived from the billing country. The prepaid ledger is always EUR.",
    )
    "Currency the tenant is invoiced and charged in (EUR, USD, GBP), derived from the billing country. The prepaid ledger is always EUR."
    created_at: datetime = Field(alias="createdAt")
    updated_at: datetime = Field(alias="updatedAt")


class BillingStatusDefaultSubscriptionCustomFeatures(BaseModel):
    """Contractual and enforced properties of a billing tier. Usage limits are
    entitlements, not features."""

    support_level: str = Field(
        alias="supportLevel",
        description="Support level (community, email, priority, dedicated).",
    )
    "Support level (community, email, priority, dedicated)."
    sla: bool = Field(description="Whether SLA guarantees apply.")
    "Whether SLA guarantees apply."
    processing_customizable: bool = Field(
        alias="processingCustomizable",
        description="Whether the tenant may override the tier's processing profiles.",
    )
    "Whether the tenant may override the tier's processing profiles."


class BillingStatusDefaultSubscriptionPricingOverrides(BaseModel):
    """A pricing rule: how a meter rates to money. Decimal fields are strings to preserve precision."""

    meter: str = Field(
        description="Canonical meter name (for example delivered_minutes, storage_gb_seconds_cold, media_seconds, or a marketplace-defined meter)."
    )
    "Canonical meter name (for example delivered_minutes, storage_gb_seconds_cold, media_seconds, or a marketplace-defined meter)."
    model: str = Field(
        description="Pricing model (tiered_graduated, all_usage, dimensioned)."
    )
    "Pricing model (tiered_graduated, all_usage, dimensioned)."
    currency: str = Field(description="Currency (ISO 4217).")
    "Currency (ISO 4217)."
    included_quantity: str = Field(
        alias="includedQuantity",
        description="Free quantity included before billing kicks in.",
    )
    "Free quantity included before billing kicks in."
    unit_price: str = Field(
        alias="unitPrice", description="Price per unit (decimal as string)."
    )
    "Price per unit (decimal as string)."
    config_json: str = Field(
        alias="configJson",
        description="Model-specific config (for example dimension selector rates); JSON-encoded.",
    )
    "Model-specific config (for example dimension selector rates); JSON-encoded."


class BillingStatusDefaultSubscriptionEntitlementOverrides(BaseModel):
    """A key-value entitlement entry. Values are JSON-encoded for type flexibility."""

    key: str
    value: str


class BillingStatusDefaultSubscriptionPendingTier(BaseModel):
    """A subscription tier defining pricing rules, entitlements, and features.
    Tenants subscribe to a tier which determines their billing and capabilities."""

    id: str = Field(description="Unique tier identifier.")
    "Unique tier identifier."
    tier_name: str = Field(
        alias="tierName",
        description="Internal tier name (payg, free, supporter, developer, production, enterprise).",
    )
    "Internal tier name (payg, free, supporter, developer, production, enterprise)."
    tier_level: int = Field(
        alias="tierLevel",
        description="Numeric ordering of tiers; 0 = payg (prepaid), 1 = free, 5 = enterprise. Used to compare upgrade vs downgrade direction.",
    )
    "Numeric ordering of tiers; 0 = payg (prepaid), 1 = free, 5 = enterprise. Used to compare upgrade vs downgrade direction."
    display_name: str = Field(alias="displayName", description="User-facing tier name.")
    "User-facing tier name."
    description: Optional[str] = Field(description="Tier description for marketing.")
    "Tier description for marketing."
    base_price: float = Field(alias="basePrice", description="Base monthly price.")
    "Base monthly price."
    currency: str = Field(description="Currency code (USD, EUR, etc.).")
    "Currency code (USD, EUR, etc.)."
    billing_period: str = Field(
        alias="billingPeriod", description="Billing period (monthly, yearly)."
    )
    "Billing period (monthly, yearly)."
    support_level: Optional[str] = Field(
        alias="supportLevel",
        description="Support level included (community, email, priority, dedicated).",
    )
    "Support level included (community, email, priority, dedicated)."
    sla_level: Optional[str] = Field(
        alias="slaLevel", description="SLA guarantee level for enterprise tiers."
    )
    "SLA guarantee level for enterprise tiers."
    metering_enabled: bool = Field(
        alias="meteringEnabled",
        description="Whether usage metering is enabled for this tier.",
    )
    "Whether usage metering is enabled for this tier."
    is_enterprise: bool = Field(
        alias="isEnterprise",
        description="Whether this is an enterprise tier with custom terms.",
    )
    "Whether this is an enterprise tier with custom terms."


class BillingStatusDefaultRecentPayments(BaseModel):
    """A tenant-visible invoice payment record. Provider secrets and raw payloads are never exposed."""

    id: str
    invoice_id: str = Field(alias="invoiceId")
    method: str
    amount: Any
    currency: Any
    status: str
    confirmed_at: Optional[datetime] = Field(alias="confirmedAt")
    created_at: datetime = Field(alias="createdAt")
    updated_at: datetime = Field(alias="updatedAt")
    conversion: Optional["BillingStatusDefaultRecentPaymentsConversion"] = Field(
        description="Amount charged and the EUR amount applied to the invoice, at the ECB rate of payment creation."
    )
    "Amount charged and the EUR amount applied to the invoice, at the ECB rate of payment creation."


class BillingStatusDefaultRecentPaymentsConversion(BaseModel):
    """Conversion of a payment into the EUR prepaid ledger and invoice currency.
    EUR amounts use the identity rate."""

    original_amount_cents: int = Field(
        alias="originalAmountCents",
        description="Amount charged, in cents of `originalCurrency`.",
    )
    "Amount charged, in cents of `originalCurrency`."
    original_currency: Any = Field(
        alias="originalCurrency", description="Currency charged (EUR, USD, GBP)."
    )
    "Currency charged (EUR, USD, GBP)."
    eur_amount_cents: int = Field(
        alias="eurAmountCents", description="EUR cents credited or applied."
    )
    "EUR cents credited or applied."
    units_per_eur: str = Field(
        alias="unitsPerEur",
        description='Units of `originalCurrency` one euro buys, as a decimal string ("1" for EUR).',
    )
    'Units of `originalCurrency` one euro buys, as a decimal string ("1" for EUR).'
    source: str = Field(
        description="Rate source: identity (EUR), ecb (ECB reference rate), or legacy_quote (USD rate quoted before ECB rates were stored)."
    )
    "Rate source: identity (EUR), ecb (ECB reference rate), or legacy_quote (USD rate quoted before ECB rates were stored)."
    reference_date: str = Field(
        alias="referenceDate",
        description="ECB reference date of the rate (YYYY-MM-DD).",
    )
    "ECB reference date of the rate (YYYY-MM-DD)."


class BillingStatusDefaultLiveUsage(BaseModel):
    tenant_id: Optional[str] = Field(alias="tenantId")
    period_start: Optional[datetime] = Field(alias="periodStart")
    period_end: Optional[datetime] = Field(alias="periodEnd")
    stream_hours: float = Field(alias="streamHours")
    egress_gb: float = Field(alias="egressGb")
    peak_bandwidth_mbps: float = Field(alias="peakBandwidthMbps")
    display_storage_gb: float = Field(alias="displayStorageGb")
    livepeer_h_264_seconds: float = Field(alias="livepeerH264Seconds")
    livepeer_vp_9_seconds: float = Field(alias="livepeerVp9Seconds")
    livepeer_av_1_seconds: float = Field(alias="livepeerAv1Seconds")
    livepeer_hevc_seconds: float = Field(alias="livepeerHevcSeconds")
    native_av_h_264_seconds: float = Field(alias="nativeAvH264Seconds")
    native_av_vp_9_seconds: float = Field(alias="nativeAvVp9Seconds")
    native_av_av_1_seconds: float = Field(alias="nativeAvAv1Seconds")
    native_av_hevc_seconds: float = Field(alias="nativeAvHevcSeconds")
    native_av_aac_seconds: float = Field(alias="nativeAvAacSeconds")
    native_av_opus_seconds: float = Field(alias="nativeAvOpusSeconds")
    total_streams: int = Field(alias="totalStreams")
    total_viewers: int = Field(alias="totalViewers")
    viewer_hours: float = Field(alias="viewerHours")
    max_viewers: int = Field(alias="maxViewers")
    unique_users: int = Field(alias="uniqueUsers")
    livepeer_segment_count: int = Field(alias="livepeerSegmentCount")
    livepeer_unique_streams: int = Field(alias="livepeerUniqueStreams")
    native_av_segment_count: int = Field(alias="nativeAvSegmentCount")
    native_av_unique_streams: int = Field(alias="nativeAvUniqueStreams")
    unique_countries: int = Field(alias="uniqueCountries")
    unique_cities: int = Field(alias="uniqueCities")
    geo_breakdown: list["BillingStatusDefaultLiveUsageGeoBreakdown"] = Field(
        alias="geoBreakdown"
    )
    clips_created: int = Field(alias="clipsCreated")
    clips_deleted: int = Field(alias="clipsDeleted")
    dvr_created: int = Field(alias="dvrCreated")
    dvr_deleted: int = Field(alias="dvrDeleted")
    vod_created: int = Field(alias="vodCreated")
    vod_deleted: int = Field(alias="vodDeleted")
    clip_bytes: int = Field(alias="clipBytes")
    dvr_bytes: int = Field(alias="dvrBytes")
    vod_bytes: int = Field(alias="vodBytes")
    frozen_clip_bytes: int = Field(alias="frozenClipBytes")
    frozen_dvr_bytes: int = Field(alias="frozenDvrBytes")
    frozen_vod_bytes: int = Field(alias="frozenVodBytes")
    synced_artifact_count: int = Field(alias="syncedArtifactCount")
    synced_artifact_bytes: int = Field(alias="syncedArtifactBytes")


class BillingStatusDefaultLiveUsageGeoBreakdown(BaseModel):
    country_code: str = Field(alias="countryCode")
    viewer_count: int = Field(alias="viewerCount")
    viewer_hours: float = Field(alias="viewerHours")
    egress_gb: float = Field(alias="egressGb")


class BillingStatusDefaultInvoicePreview(BaseModel):
    """A billing invoice for a subscription period.
    Includes base subscription and metered usage charges. Amounts are computed in
    EUR; a finalized invoice is presented and charged in the tenant's presentment
    currency at the ECB rate of its finalization date."""

    id: str = Field(description="Unique invoice identifier.")
    "Unique invoice identifier."
    amount: Any = Field(description="Total amount due in EUR (after credits applied).")
    "Total amount due in EUR (after credits applied)."
    base_amount: Any = Field(
        alias="baseAmount", description="Base subscription amount in EUR."
    )
    "Base subscription amount in EUR."
    metered_amount: Any = Field(
        alias="meteredAmount",
        description="Metered usage charges in EUR (net; 0 while usage is waived during beta).",
    )
    "Metered usage charges in EUR (net; 0 while usage is waived during beta)."
    gross_metered_amount: Any = Field(
        alias="grossMeteredAmount",
        description="Unwaived metered total — what usage would have cost. Equals meteredAmount when usage is not waived; the would-have-cost figure when it is.",
    )
    "Unwaived metered total — what usage would have cost. Equals meteredAmount when usage is not waived; the would-have-cost figure when it is."
    prepaid_credit_applied: Any = Field(
        alias="prepaidCreditApplied",
        description="Prepaid balance credit applied to this invoice, in EUR.",
    )
    "Prepaid balance credit applied to this invoice, in EUR."
    currency: Any = Field(description="Currency of the amounts above, EUR.")
    "Currency of the amounts above, EUR."
    presentment_amount_cents: Optional[int] = Field(
        alias="presentmentAmountCents",
        description="Total charged, in cents of `presentmentCurrency`. Null until the invoice is finalized.",
    )
    "Total charged, in cents of `presentmentCurrency`. Null until the invoice is finalized."
    presentment_currency: Optional[str] = Field(
        alias="presentmentCurrency",
        description="Currency the invoice is presented and charged in (EUR, USD, GBP). Empty until the invoice is finalized.",
    )
    "Currency the invoice is presented and charged in (EUR, USD, GBP). Empty until the invoice is finalized."
    presentment_units_per_eur: Optional[str] = Field(
        alias="presentmentUnitsPerEur",
        description="Units of `presentmentCurrency` one euro buys at finalization, as a decimal string. Empty until the invoice is finalized.",
    )
    "Units of `presentmentCurrency` one euro buys at finalization, as a decimal string. Empty until the invoice is finalized."
    presentment_reference_date: Optional[str] = Field(
        alias="presentmentReferenceDate",
        description="ECB reference date of the presentment rate (YYYY-MM-DD). Empty until the invoice is finalized.",
    )
    "ECB reference date of the presentment rate (YYYY-MM-DD). Empty until the invoice is finalized."
    finalized_at: Optional[datetime] = Field(
        alias="finalizedAt",
        description="When the invoice was finalized and its presentment amount fixed.",
    )
    "When the invoice was finalized and its presentment amount fixed."
    status: InvoiceStatus = Field(
        description="Invoice status (draft, open, paid, void)."
    )
    "Invoice status (draft, open, paid, void)."
    due_date: datetime = Field(alias="dueDate", description="Payment due date.")
    "Payment due date."
    paid_at: Optional[datetime] = Field(
        alias="paidAt", description="When payment was received."
    )
    "When payment was received."
    created_at: datetime = Field(
        alias="createdAt", description="When the invoice was created."
    )
    "When the invoice was created."
    updated_at: datetime = Field(
        alias="updatedAt", description="When the invoice was last updated."
    )
    "When the invoice was last updated."
    period_start: Optional[datetime] = Field(
        alias="periodStart", description="Billing period start date."
    )
    "Billing period start date."
    period_end: Optional[datetime] = Field(
        alias="periodEnd", description="Billing period end date."
    )
    "Billing period end date."
    usage_details: Optional[Any] = Field(
        alias="usageDetails", description="Detailed usage breakdown (JSON)."
    )
    "Detailed usage breakdown (JSON)."
    line_items: list["BillingStatusDefaultInvoicePreviewLineItems"] = Field(
        alias="lineItems", description="Individual line items on the invoice."
    )
    "Individual line items on the invoice."


class BillingStatusDefaultInvoicePreviewLineItems(BaseModel):
    """A single line item on an invoice or usage preview, produced by the rating engine.
    Decimal quantities are strings to preserve precision."""

    line_key: str = Field(
        alias="lineKey",
        description="Stable identity: 'base_subscription', 'meter:<name>:<cluster_id>:<yyyymm>'.",
    )
    "Stable identity: 'base_subscription', 'meter:<name>:<cluster_id>:<yyyymm>'."
    meter: str = Field(description="Meter name; empty for base_subscription.")
    "Meter name; empty for base_subscription."
    description: str = Field(description="Description of the charge.")
    "Description of the charge."
    quantity: str = Field(description="Total quantity used (decimal as string).")
    "Total quantity used (decimal as string)."
    included_quantity: str = Field(
        alias="includedQuantity", description="Free quantity (decimal as string)."
    )
    "Free quantity (decimal as string)."
    billable_quantity: str = Field(
        alias="billableQuantity", description="Billable quantity (decimal as string)."
    )
    "Billable quantity (decimal as string)."
    unit_price: str = Field(
        alias="unitPrice", description="Price per unit (decimal as string)."
    )
    "Price per unit (decimal as string)."
    total: str = Field(
        description="Total for this line item (= billableQuantity * unitPrice), decimal as string."
    )
    "Total for this line item (= billableQuantity * unitPrice), decimal as string."
    currency: str = Field(description="Currency (ISO 4217).")
    "Currency (ISO 4217)."
    cluster_id: Optional[str] = Field(
        alias="clusterId",
        description="Cluster id this line was attributed to; null for tenant-scoped lines (base_subscription).",
    )
    "Cluster id this line was attributed to; null for tenant-scoped lines (base_subscription)."
    cluster_name: Optional[str] = Field(
        alias="clusterName",
        description="Display name of the cluster, joined from Quartermaster at read time. Null when clusterId is null.",
    )
    "Display name of the cluster, joined from Quartermaster at read time. Null when clusterId is null."
    cluster_kind: Optional[str] = Field(
        alias="clusterKind",
        description="platform_official | tenant_private | third_party_marketplace; null when clusterId is null.",
    )
    "platform_official | tenant_private | third_party_marketplace; null when clusterId is null."
    pricing_source: str = Field(
        alias="pricingSource",
        description="Why the line was priced as it was: tier, cluster_metered, cluster_monthly, cluster_custom, free_unmetered, self_hosted, included_subscription.",
    )
    "Why the line was priced as it was: tier, cluster_metered, cluster_monthly, cluster_custom, free_unmetered, self_hosted, included_subscription."
    pricing_label: str = Field(
        alias="pricingLabel",
        description="Human-readable label of pricingSource (e.g. 'Self-hosted (no charge)', 'Marketplace metered'). Empty when not populated by the gateway.",
    )
    "Human-readable label of pricingSource (e.g. 'Self-hosted (no charge)', 'Marketplace metered'). Empty when not populated by the gateway."
    unit: str = Field(description="Canonical quantity unit.")
    "Canonical quantity unit."
    dimensions: Any = Field(
        description="Bounded pricing dimensions such as codec, rendition, backend, or model."
    )
    "Bounded pricing dimensions such as codec, rendition, backend, or model."


class BillingTierDefault(BaseModel):
    """A subscription tier defining pricing rules, entitlements, and features.
    Tenants subscribe to a tier which determines their billing and capabilities."""

    id: str = Field(description="Unique tier identifier.")
    "Unique tier identifier."
    tier_name: str = Field(
        alias="tierName",
        description="Internal tier name (payg, free, supporter, developer, production, enterprise).",
    )
    "Internal tier name (payg, free, supporter, developer, production, enterprise)."
    tier_level: int = Field(
        alias="tierLevel",
        description="Numeric ordering of tiers; 0 = payg (prepaid), 1 = free, 5 = enterprise. Used to compare upgrade vs downgrade direction.",
    )
    "Numeric ordering of tiers; 0 = payg (prepaid), 1 = free, 5 = enterprise. Used to compare upgrade vs downgrade direction."
    display_name: str = Field(alias="displayName", description="User-facing tier name.")
    "User-facing tier name."
    description: Optional[str] = Field(description="Tier description for marketing.")
    "Tier description for marketing."
    base_price: float = Field(alias="basePrice", description="Base monthly price.")
    "Base monthly price."
    currency: str = Field(description="Currency code (USD, EUR, etc.).")
    "Currency code (USD, EUR, etc.)."
    billing_period: str = Field(
        alias="billingPeriod", description="Billing period (monthly, yearly)."
    )
    "Billing period (monthly, yearly)."
    features: "BillingTierDefaultFeatures" = Field(
        description="Feature flags for this tier."
    )
    "Feature flags for this tier."
    pricing_rules: list["BillingTierDefaultPricingRules"] = Field(
        alias="pricingRules",
        description="Priced behaviors per meter (rating engine input).",
    )
    "Priced behaviors per meter (rating engine input)."
    entitlements: list["BillingTierDefaultEntitlements"] = Field(
        description="Non-billing grants (e.g. recording_retention_days). Values are JSON-encoded strings."
    )
    "Non-billing grants (e.g. recording_retention_days). Values are JSON-encoded strings."
    support_level: Optional[str] = Field(
        alias="supportLevel",
        description="Support level included (community, email, priority, dedicated).",
    )
    "Support level included (community, email, priority, dedicated)."
    sla_level: Optional[str] = Field(
        alias="slaLevel", description="SLA guarantee level for enterprise tiers."
    )
    "SLA guarantee level for enterprise tiers."
    metering_enabled: bool = Field(
        alias="meteringEnabled",
        description="Whether usage metering is enabled for this tier.",
    )
    "Whether usage metering is enabled for this tier."
    is_enterprise: bool = Field(
        alias="isEnterprise",
        description="Whether this is an enterprise tier with custom terms.",
    )
    "Whether this is an enterprise tier with custom terms."


class BillingTierDefaultFeatures(BaseModel):
    """Contractual and enforced properties of a billing tier. Usage limits are
    entitlements, not features."""

    support_level: str = Field(
        alias="supportLevel",
        description="Support level (community, email, priority, dedicated).",
    )
    "Support level (community, email, priority, dedicated)."
    sla: bool = Field(description="Whether SLA guarantees apply.")
    "Whether SLA guarantees apply."
    processing_customizable: bool = Field(
        alias="processingCustomizable",
        description="Whether the tenant may override the tier's processing profiles.",
    )
    "Whether the tenant may override the tier's processing profiles."


class BillingTierDefaultPricingRules(BaseModel):
    """A pricing rule: how a meter rates to money. Decimal fields are strings to preserve precision."""

    meter: str = Field(
        description="Canonical meter name (for example delivered_minutes, storage_gb_seconds_cold, media_seconds, or a marketplace-defined meter)."
    )
    "Canonical meter name (for example delivered_minutes, storage_gb_seconds_cold, media_seconds, or a marketplace-defined meter)."
    model: str = Field(
        description="Pricing model (tiered_graduated, all_usage, dimensioned)."
    )
    "Pricing model (tiered_graduated, all_usage, dimensioned)."
    currency: str = Field(description="Currency (ISO 4217).")
    "Currency (ISO 4217)."
    included_quantity: str = Field(
        alias="includedQuantity",
        description="Free quantity included before billing kicks in.",
    )
    "Free quantity included before billing kicks in."
    unit_price: str = Field(
        alias="unitPrice", description="Price per unit (decimal as string)."
    )
    "Price per unit (decimal as string)."
    config_json: str = Field(
        alias="configJson",
        description="Model-specific config (for example dimension selector rates); JSON-encoded.",
    )
    "Model-specific config (for example dimension selector rates); JSON-encoded."


class BillingTierDefaultEntitlements(BaseModel):
    """A key-value entitlement entry. Values are JSON-encoded for type flexibility."""

    key: str
    value: str


class BootstrapEdgeResponseDefault(BaseModel):
    """Result of a successful bootstrapEdge call. Carries everything the edge
    needs to come online: its identity, its assigned Foghorn for runtime
    traffic, and (if the cluster issues per-node certs) its initial TLS
    material plus the cluster's internal CA bundle."""

    node_id: str = Field(alias="nodeId")
    edge_domain: str = Field(alias="edgeDomain")
    pool_domain: Optional[str] = Field(alias="poolDomain")
    cluster_slug: str = Field(alias="clusterSlug")
    cluster_id: str = Field(alias="clusterId")
    foghorn_grpc_addr: str = Field(alias="foghornGrpcAddr")
    cert_pem: Optional[str] = Field(alias="certPem")
    key_pem: Optional[str] = Field(alias="keyPem")
    internal_ca_bundle: Optional[str] = Field(alias="internalCaBundle")
    telemetry: Optional["BootstrapEdgeResponseDefaultTelemetry"]


class BootstrapEdgeResponseDefaultTelemetry(BaseModel):
    enabled: bool
    write_url: Optional[str] = Field(alias="writeUrl")
    bearer_token: Optional[str] = Field(alias="bearerToken")


class BufferEventDefault(BaseModel):
    id: str
    event_id: str = Field(alias="eventId")
    timestamp: datetime
    node_id: Optional[str] = Field(alias="nodeId")
    buffer_state: BufferState = Field(alias="bufferState")
    event_data: Optional[str] = Field(alias="eventData")
    payload: Optional[Any]


class BufferEventInNodeDefault(BaseModel):
    id: str
    event_id: str = Field(alias="eventId")
    timestamp: datetime
    buffer_event_node_id: Optional[str] = Field(alias="bufferEventNodeId")
    buffer_state: BufferState = Field(alias="bufferState")
    event_data: Optional[str] = Field(alias="eventData")
    payload: Optional[Any]


class CapabilitiesDefault(BaseModel):
    """Enforced gates for the current tenant. Each section is read from the service
    that enforces it and cached per tenant for 30 seconds. When that service is
    unavailable, the last cached section is returned; without one the section is
    null and the response carries an error."""

    tenant: Optional["CapabilitiesDefaultTenant"]
    clusters: Optional[list["CapabilitiesDefaultClusters"]]
    observed_at: datetime = Field(
        alias="observedAt",
        description="When the oldest returned section was read from its source.",
    )
    "When the oldest returned section was read from its source."


class CapabilitiesDefaultTenant(BaseModel):
    """Tenant-wide gates."""

    platform_operator: bool = Field(
        alias="platformOperator",
        description="Whether the caller holds the platform operator grant.",
    )
    "Whether the caller holds the platform operator grant."
    recording_retention: "CapabilitiesDefaultTenantRecordingRetention" = Field(
        alias="recordingRetention",
        description="Upper bound on recording retention the tenant can set.",
    )
    "Upper bound on recording retention the tenant can set."
    processing_customizable: bool = Field(
        alias="processingCustomizable",
        description="Whether the tenant may override the tier's processing profiles.",
    )
    "Whether the tenant may override the tier's processing profiles."
    custom_subdomain: bool = Field(
        alias="customSubdomain",
        description="Whether the tenant's custom subdomain alias is published.",
    )
    "Whether the tenant's custom subdomain alias is published."
    custom_domain: bool = Field(
        alias="customDomain",
        description="Whether the tenant's custom domain is published.",
    )
    "Whether the tenant's custom domain is published."


class CapabilitiesDefaultTenantRecordingRetention(BaseModel):
    """Recording retention bound from the tenant's billing tier."""

    capped: bool = Field(description="Whether a maximum applies.")
    "Whether a maximum applies."
    max_days: Optional[int] = Field(
        alias="maxDays", description="Maximum retention in days when capped."
    )
    "Maximum retention in days when capped."


class CapabilitiesDefaultClusters(BaseModel):
    """A cluster the tenant is entitled to route to."""

    cluster_id: str = Field(alias="clusterId")
    cluster_name: str = Field(alias="clusterName")
    role: str = Field(description="preferred, official, or subscribed.")
    "preferred, official, or subscribed."
    access_level: str = Field(
        alias="accessLevel",
        description="Access level of the tenant's grant (shared, dedicated, priority).",
    )
    "Access level of the tenant's grant (shared, dedicated, priority)."
    media: "CapabilitiesDefaultClustersMedia"


class CapabilitiesDefaultClustersMedia(BaseModel):
    """Media verbs a cluster accepts for the tenant now. Each is true only when the
    capacity owner's consent permits it and at least one active edge node reported
    the capability within the health freshness window, so it can lag by up to that
    window."""

    ingest: bool
    playback: bool
    storage: bool
    processing: bool


class CardTopupResultDefault(BaseModel):
    """Result from creating a card top-up checkout session."""

    topup_id: str = Field(
        alias="topupId", description="Internal top-up ID for tracking."
    )
    "Internal top-up ID for tracking."
    checkout_url: str = Field(
        alias="checkoutUrl", description="URL to redirect user for checkout."
    )
    "URL to redirect user for checkout."
    expires_at: datetime = Field(
        alias="expiresAt", description="When the checkout session expires."
    )
    "When the checkout session expires."
    amount_cents: int = Field(
        alias="amountCents", description="Amount charged, in cents of `currency`."
    )
    "Amount charged, in cents of `currency`."
    currency: Any = Field(
        description="Tenant presentment currency the checkout charges in (EUR, USD, GBP)."
    )
    "Tenant presentment currency the checkout charges in (EUR, USD, GBP)."
    conversion: Optional["CardTopupResultDefaultConversion"] = Field(
        description="EUR amount the top-up credits, locked at the ECB rate when the checkout was created."
    )
    "EUR amount the top-up credits, locked at the ECB rate when the checkout was created."


class CardTopupResultDefaultConversion(BaseModel):
    """Conversion of a payment into the EUR prepaid ledger and invoice currency.
    EUR amounts use the identity rate."""

    original_amount_cents: int = Field(
        alias="originalAmountCents",
        description="Amount charged, in cents of `originalCurrency`.",
    )
    "Amount charged, in cents of `originalCurrency`."
    original_currency: Any = Field(
        alias="originalCurrency", description="Currency charged (EUR, USD, GBP)."
    )
    "Currency charged (EUR, USD, GBP)."
    eur_amount_cents: int = Field(
        alias="eurAmountCents", description="EUR cents credited or applied."
    )
    "EUR cents credited or applied."
    units_per_eur: str = Field(
        alias="unitsPerEur",
        description='Units of `originalCurrency` one euro buys, as a decimal string ("1" for EUR).',
    )
    'Units of `originalCurrency` one euro buys, as a decimal string ("1" for EUR).'
    source: str = Field(
        description="Rate source: identity (EUR), ecb (ECB reference rate), or legacy_quote (USD rate quoted before ECB rates were stored)."
    )
    "Rate source: identity (EUR), ecb (ECB reference rate), or legacy_quote (USD rate quoted before ECB rates were stored)."
    reference_date: str = Field(
        alias="referenceDate",
        description="ECB reference date of the rate (YYYY-MM-DD).",
    )
    "ECB reference date of the rate (YYYY-MM-DD)."


class ChangeBillingTierPayloadDefault(BaseModel):
    """Result of a postpaid tier change. Either appliedTier is set (immediate upgrade)
    or pendingTier + effectiveAt are set (scheduled downgrade at period end)."""

    success: bool = Field(description="Whether the request was accepted.")
    "Whether the request was accepted."
    message: str = Field(description="Human-readable status message.")
    "Human-readable status message."
    applied_tier: Optional["ChangeBillingTierPayloadDefaultAppliedTier"] = Field(
        alias="appliedTier",
        description="Set on immediate upgrade — the new active tier.",
    )
    "Set on immediate upgrade — the new active tier."
    pending_tier: Optional["ChangeBillingTierPayloadDefaultPendingTier"] = Field(
        alias="pendingTier",
        description="Set on scheduled downgrade — the staged target tier.",
    )
    "Set on scheduled downgrade — the staged target tier."
    effective_at: Optional[datetime] = Field(
        alias="effectiveAt", description="When a scheduled downgrade will take effect."
    )
    "When a scheduled downgrade will take effect."


class ChangeBillingTierPayloadDefaultAppliedTier(BaseModel):
    """A subscription tier defining pricing rules, entitlements, and features.
    Tenants subscribe to a tier which determines their billing and capabilities."""

    id: str = Field(description="Unique tier identifier.")
    "Unique tier identifier."
    tier_name: str = Field(
        alias="tierName",
        description="Internal tier name (payg, free, supporter, developer, production, enterprise).",
    )
    "Internal tier name (payg, free, supporter, developer, production, enterprise)."
    tier_level: int = Field(
        alias="tierLevel",
        description="Numeric ordering of tiers; 0 = payg (prepaid), 1 = free, 5 = enterprise. Used to compare upgrade vs downgrade direction.",
    )
    "Numeric ordering of tiers; 0 = payg (prepaid), 1 = free, 5 = enterprise. Used to compare upgrade vs downgrade direction."
    display_name: str = Field(alias="displayName", description="User-facing tier name.")
    "User-facing tier name."
    description: Optional[str] = Field(description="Tier description for marketing.")
    "Tier description for marketing."
    base_price: float = Field(alias="basePrice", description="Base monthly price.")
    "Base monthly price."
    currency: str = Field(description="Currency code (USD, EUR, etc.).")
    "Currency code (USD, EUR, etc.)."
    billing_period: str = Field(
        alias="billingPeriod", description="Billing period (monthly, yearly)."
    )
    "Billing period (monthly, yearly)."
    features: "ChangeBillingTierPayloadDefaultAppliedTierFeatures" = Field(
        description="Feature flags for this tier."
    )
    "Feature flags for this tier."
    pricing_rules: list["ChangeBillingTierPayloadDefaultAppliedTierPricingRules"] = (
        Field(
            alias="pricingRules",
            description="Priced behaviors per meter (rating engine input).",
        )
    )
    "Priced behaviors per meter (rating engine input)."
    entitlements: list["ChangeBillingTierPayloadDefaultAppliedTierEntitlements"] = (
        Field(
            description="Non-billing grants (e.g. recording_retention_days). Values are JSON-encoded strings."
        )
    )
    "Non-billing grants (e.g. recording_retention_days). Values are JSON-encoded strings."
    support_level: Optional[str] = Field(
        alias="supportLevel",
        description="Support level included (community, email, priority, dedicated).",
    )
    "Support level included (community, email, priority, dedicated)."
    sla_level: Optional[str] = Field(
        alias="slaLevel", description="SLA guarantee level for enterprise tiers."
    )
    "SLA guarantee level for enterprise tiers."
    metering_enabled: bool = Field(
        alias="meteringEnabled",
        description="Whether usage metering is enabled for this tier.",
    )
    "Whether usage metering is enabled for this tier."
    is_enterprise: bool = Field(
        alias="isEnterprise",
        description="Whether this is an enterprise tier with custom terms.",
    )
    "Whether this is an enterprise tier with custom terms."


class ChangeBillingTierPayloadDefaultAppliedTierFeatures(BaseModel):
    """Contractual and enforced properties of a billing tier. Usage limits are
    entitlements, not features."""

    support_level: str = Field(
        alias="supportLevel",
        description="Support level (community, email, priority, dedicated).",
    )
    "Support level (community, email, priority, dedicated)."
    sla: bool = Field(description="Whether SLA guarantees apply.")
    "Whether SLA guarantees apply."
    processing_customizable: bool = Field(
        alias="processingCustomizable",
        description="Whether the tenant may override the tier's processing profiles.",
    )
    "Whether the tenant may override the tier's processing profiles."


class ChangeBillingTierPayloadDefaultAppliedTierPricingRules(BaseModel):
    """A pricing rule: how a meter rates to money. Decimal fields are strings to preserve precision."""

    meter: str = Field(
        description="Canonical meter name (for example delivered_minutes, storage_gb_seconds_cold, media_seconds, or a marketplace-defined meter)."
    )
    "Canonical meter name (for example delivered_minutes, storage_gb_seconds_cold, media_seconds, or a marketplace-defined meter)."
    model: str = Field(
        description="Pricing model (tiered_graduated, all_usage, dimensioned)."
    )
    "Pricing model (tiered_graduated, all_usage, dimensioned)."
    currency: str = Field(description="Currency (ISO 4217).")
    "Currency (ISO 4217)."
    included_quantity: str = Field(
        alias="includedQuantity",
        description="Free quantity included before billing kicks in.",
    )
    "Free quantity included before billing kicks in."
    unit_price: str = Field(
        alias="unitPrice", description="Price per unit (decimal as string)."
    )
    "Price per unit (decimal as string)."
    config_json: str = Field(
        alias="configJson",
        description="Model-specific config (for example dimension selector rates); JSON-encoded.",
    )
    "Model-specific config (for example dimension selector rates); JSON-encoded."


class ChangeBillingTierPayloadDefaultAppliedTierEntitlements(BaseModel):
    """A key-value entitlement entry. Values are JSON-encoded for type flexibility."""

    key: str
    value: str


class ChangeBillingTierPayloadDefaultPendingTier(BaseModel):
    """A subscription tier defining pricing rules, entitlements, and features.
    Tenants subscribe to a tier which determines their billing and capabilities."""

    id: str = Field(description="Unique tier identifier.")
    "Unique tier identifier."
    tier_name: str = Field(
        alias="tierName",
        description="Internal tier name (payg, free, supporter, developer, production, enterprise).",
    )
    "Internal tier name (payg, free, supporter, developer, production, enterprise)."
    tier_level: int = Field(
        alias="tierLevel",
        description="Numeric ordering of tiers; 0 = payg (prepaid), 1 = free, 5 = enterprise. Used to compare upgrade vs downgrade direction.",
    )
    "Numeric ordering of tiers; 0 = payg (prepaid), 1 = free, 5 = enterprise. Used to compare upgrade vs downgrade direction."
    display_name: str = Field(alias="displayName", description="User-facing tier name.")
    "User-facing tier name."
    description: Optional[str] = Field(description="Tier description for marketing.")
    "Tier description for marketing."
    base_price: float = Field(alias="basePrice", description="Base monthly price.")
    "Base monthly price."
    currency: str = Field(description="Currency code (USD, EUR, etc.).")
    "Currency code (USD, EUR, etc.)."
    billing_period: str = Field(
        alias="billingPeriod", description="Billing period (monthly, yearly)."
    )
    "Billing period (monthly, yearly)."
    features: "ChangeBillingTierPayloadDefaultPendingTierFeatures" = Field(
        description="Feature flags for this tier."
    )
    "Feature flags for this tier."
    pricing_rules: list["ChangeBillingTierPayloadDefaultPendingTierPricingRules"] = (
        Field(
            alias="pricingRules",
            description="Priced behaviors per meter (rating engine input).",
        )
    )
    "Priced behaviors per meter (rating engine input)."
    entitlements: list["ChangeBillingTierPayloadDefaultPendingTierEntitlements"] = (
        Field(
            description="Non-billing grants (e.g. recording_retention_days). Values are JSON-encoded strings."
        )
    )
    "Non-billing grants (e.g. recording_retention_days). Values are JSON-encoded strings."
    support_level: Optional[str] = Field(
        alias="supportLevel",
        description="Support level included (community, email, priority, dedicated).",
    )
    "Support level included (community, email, priority, dedicated)."
    sla_level: Optional[str] = Field(
        alias="slaLevel", description="SLA guarantee level for enterprise tiers."
    )
    "SLA guarantee level for enterprise tiers."
    metering_enabled: bool = Field(
        alias="meteringEnabled",
        description="Whether usage metering is enabled for this tier.",
    )
    "Whether usage metering is enabled for this tier."
    is_enterprise: bool = Field(
        alias="isEnterprise",
        description="Whether this is an enterprise tier with custom terms.",
    )
    "Whether this is an enterprise tier with custom terms."


class ChangeBillingTierPayloadDefaultPendingTierFeatures(BaseModel):
    """Contractual and enforced properties of a billing tier. Usage limits are
    entitlements, not features."""

    support_level: str = Field(
        alias="supportLevel",
        description="Support level (community, email, priority, dedicated).",
    )
    "Support level (community, email, priority, dedicated)."
    sla: bool = Field(description="Whether SLA guarantees apply.")
    "Whether SLA guarantees apply."
    processing_customizable: bool = Field(
        alias="processingCustomizable",
        description="Whether the tenant may override the tier's processing profiles.",
    )
    "Whether the tenant may override the tier's processing profiles."


class ChangeBillingTierPayloadDefaultPendingTierPricingRules(BaseModel):
    """A pricing rule: how a meter rates to money. Decimal fields are strings to preserve precision."""

    meter: str = Field(
        description="Canonical meter name (for example delivered_minutes, storage_gb_seconds_cold, media_seconds, or a marketplace-defined meter)."
    )
    "Canonical meter name (for example delivered_minutes, storage_gb_seconds_cold, media_seconds, or a marketplace-defined meter)."
    model: str = Field(
        description="Pricing model (tiered_graduated, all_usage, dimensioned)."
    )
    "Pricing model (tiered_graduated, all_usage, dimensioned)."
    currency: str = Field(description="Currency (ISO 4217).")
    "Currency (ISO 4217)."
    included_quantity: str = Field(
        alias="includedQuantity",
        description="Free quantity included before billing kicks in.",
    )
    "Free quantity included before billing kicks in."
    unit_price: str = Field(
        alias="unitPrice", description="Price per unit (decimal as string)."
    )
    "Price per unit (decimal as string)."
    config_json: str = Field(
        alias="configJson",
        description="Model-specific config (for example dimension selector rates); JSON-encoded.",
    )
    "Model-specific config (for example dimension selector rates); JSON-encoded."


class ChangeBillingTierPayloadDefaultPendingTierEntitlements(BaseModel):
    """A key-value entitlement entry. Values are JSON-encoded for type flexibility."""

    key: str
    value: str


class ClientMetrics5mDefault(BaseModel):
    id: str
    timestamp: datetime
    stream_id: str = Field(alias="streamId")
    stream: Optional["ClientMetrics5mDefaultStream"]
    node_id: str = Field(alias="nodeId")
    active_sessions: int = Field(alias="activeSessions")
    avg_bandwidth_in: float = Field(alias="avgBandwidthIn")
    avg_bandwidth_out: float = Field(alias="avgBandwidthOut")
    avg_connection_time: float = Field(alias="avgConnectionTime")
    packet_loss_rate: Optional[float] = Field(alias="packetLossRate")


class ClientMetrics5mDefaultStream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

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
        description="Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path.",
    )
    "Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path."
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
    pull_source: Optional["ClientMetrics5mDefaultStreamPullSource"] = Field(
        alias="pullSource",
        description="Pull-source config for pull streams; null for push streams.",
    )
    "Pull-source config for pull streams; null for push streams."
    managed_source: Optional["ClientMetrics5mDefaultStreamManagedSource"] = Field(
        alias="managedSource",
        description="Safe source summary for managed streams; null for push and pull streams.",
    )
    "Safe source summary for managed streams; null for push and pull streams."
    source_location: "ClientMetrics5mDefaultStreamSourceLocation" = Field(
        alias="sourceLocation",
        description="Where the stream's source may be ingested, derived from the stream's own ingest placement rules.",
    )
    "Where the stream's source may be ingested, derived from the stream's own ingest placement rules."
    created_at: datetime = Field(
        alias="createdAt", description="When this stream was created."
    )
    "When this stream was created."
    updated_at: datetime = Field(
        alias="updatedAt", description="When this stream was last modified."
    )
    "When this stream was last modified."
    metrics: Optional["ClientMetrics5mDefaultStreamMetrics"] = Field(
        description="Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    )
    "Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    push_targets: list["ClientMetrics5mDefaultStreamPushTargets"] = Field(
        alias="pushTargets",
        description="Configured multistream push targets for this stream.",
    )
    "Configured multistream push targets for this stream."
    playback_policy: Optional["ClientMetrics5mDefaultStreamPlaybackPolicy"] = Field(
        alias="playbackPolicy",
        description="Playback access policy. null/PUBLIC = anyone with the playbackId can watch.",
    )
    "Playback access policy. null/PUBLIC = anyone with the playbackId can watch."
    dvr_chapter_mode: Optional[DVRChapterMode] = Field(
        alias="dvrChapterMode",
        description="How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast.",
    )
    "How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast."
    dvr_chapter_interval_seconds: Optional[int] = Field(
        alias="dvrChapterIntervalSeconds",
        description="Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour).",
    )
    "Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour)."
    monitoring: MonitoringToggle = Field(
        description="Per-stream Skipper monitoring override (INHERIT follows tier)."
    )
    "Per-stream Skipper monitoring override (INHERIT follows tier)."
    thumbnail_assets: Optional["ClientMetrics5mDefaultStreamThumbnailAssets"] = Field(
        alias="thumbnailAssets",
        description="Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles.",
    )
    "Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles."
    retention_overrides: Optional["ClientMetrics5mDefaultStreamRetentionOverrides"] = (
        Field(
            alias="retentionOverrides",
            description="Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here.",
        )
    )
    "Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here."


class ClientMetrics5mDefaultStreamPullSource(BaseModel):
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


class ClientMetrics5mDefaultStreamManagedSource(BaseModel):
    """Safe summary of an operator-managed source. Literal paths, commands, and
    credentials are intentionally not exposed through the tenant API."""

    source_kind: str = Field(
        alias="sourceKind", description="Managed source form: file, playlist, or exec."
    )
    "Managed source form: file, playlist, or exec."
    always_on: bool = Field(
        alias="alwaysOn",
        description="Whether the source is kept active without waiting for a viewer.",
    )
    "Whether the source is kept active without waiting for a viewer."
    placement_count: int = Field(
        alias="placementCount",
        description="Number of source placements requested by the operator configuration.",
    )
    "Number of source placements requested by the operator configuration."


class ClientMetrics5mDefaultStreamSourceLocation(BaseModel):
    mode: SourceLocationMode
    avoid_node_ids: list[str] = Field(
        alias="avoidNodeIds", description="Empty unless mode is RESTRICTED."
    )
    "Empty unless mode is RESTRICTED."


class ClientMetrics5mDefaultStreamMetrics(BaseModel):
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
    node_id: Optional[str] = Field(
        alias="nodeId", description="ID of the edge node handling this stream."
    )
    "ID of the edge node handling this stream."
    track_count: Optional[int] = Field(
        alias="trackCount", description="Number of quality tracks available."
    )
    "Number of quality tracks available."
    total_inputs: Optional[int] = Field(
        alias="totalInputs", description="Total ingest connections to this stream."
    )
    "Total ingest connections to this stream."
    uploaded_bytes: float = Field(
        alias="uploadedBytes",
        description="Total bytes uploaded (ingested) for current session.",
    )
    "Total bytes uploaded (ingested) for current session."
    downloaded_bytes: float = Field(
        alias="downloadedBytes",
        description="Total bytes downloaded (egress) for current session.",
    )
    "Total bytes downloaded (egress) for current session."
    viewer_seconds: float = Field(
        alias="viewerSeconds", description="Total viewer-seconds accumulated."
    )
    "Total viewer-seconds accumulated."
    packets_sent: Optional[float] = Field(
        alias="packetsSent", description="Total packets sent to viewers."
    )
    "Total packets sent to viewers."
    packets_lost: Optional[float] = Field(
        alias="packetsLost", description="Total packets lost in transit."
    )
    "Total packets lost in transit."
    packets_retransmitted: Optional[float] = Field(
        alias="packetsRetransmitted", description="Total packets retransmitted."
    )
    "Total packets retransmitted."
    buffer_state: Optional[str] = Field(
        alias="bufferState",
        description="Buffer health state (HEALTHY, WARNING, CRITICAL).",
    )
    "Buffer health state (HEALTHY, WARNING, CRITICAL)."
    quality_tier: Optional[str] = Field(
        alias="qualityTier",
        description="Highest quality tier available (4K, 1080p, 720p, etc.).",
    )
    "Highest quality tier available (4K, 1080p, 720p, etc.)."
    primary_width: Optional[int] = Field(
        alias="primaryWidth", description="Primary video track width in pixels."
    )
    "Primary video track width in pixels."
    primary_height: Optional[int] = Field(
        alias="primaryHeight", description="Primary video track height in pixels."
    )
    "Primary video track height in pixels."
    primary_fps: Optional[float] = Field(
        alias="primaryFps", description="Primary video track framerate."
    )
    "Primary video track framerate."
    primary_codec: Optional[str] = Field(
        alias="primaryCodec",
        description="Primary video codec (H.264, H.265, VP9, AV1).",
    )
    "Primary video codec (H.264, H.265, VP9, AV1)."
    primary_bitrate: Optional[int] = Field(
        alias="primaryBitrate", description="Primary video bitrate in kbps."
    )
    "Primary video bitrate in kbps."
    has_issues: Optional[bool] = Field(
        alias="hasIssues", description="Whether the stream has active quality issues."
    )
    "Whether the stream has active quality issues."
    issues_description: Optional[str] = Field(
        alias="issuesDescription",
        description="Human-readable description of current issues.",
    )
    "Human-readable description of current issues."


class ClientMetrics5mDefaultStreamPushTargets(BaseModel):
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


class ClientMetrics5mDefaultStreamPlaybackPolicy(BaseModel):
    """Per-playback-object access policy. Foghorn reads this in the USER_NEW
    trigger handler; the requiresAuth marker (on the playback object itself)
    gates whether the full policy is fetched at all."""

    type_: PlaybackPolicyType = Field(alias="type")
    allowed_origins: list[str] = Field(
        alias="allowedOrigins",
        description="Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied.",
    )
    "Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied."


class ClientMetrics5mDefaultStreamThumbnailAssets(BaseModel):
    """Chandler-served thumbnail asset URLs (poster, sprite, VTT cues). URL shape
    is `{chandlerBase}/assets/{assetKey}/poster.jpg`,
    `/assets/{assetKey}/sprite.jpg`, `/assets/{assetKey}/sprite.vtt` — Chandler
    serves the object key directly, no version resolution. assetKey is stream_id
    for live streams; clip_hash / dvr_hash / vod_hash (= artifact_hash) for artifacts."""

    poster_url: str = Field(alias="posterUrl")
    sprite_vtt_url: str = Field(alias="spriteVttUrl")
    sprite_jpg_url: str = Field(alias="spriteJpgUrl")
    asset_key: str = Field(alias="assetKey")


class ClientMetrics5mDefaultStreamRetentionOverrides(BaseModel):
    """Per-stream retention overrides for DVR and clips. VOD uploads aren't
    stream-bound, so they have no stream-level knob here."""

    stream_id: str = Field(alias="streamId")
    dvr_retention_days_override: Optional[int] = Field(
        alias="dvrRetentionDaysOverride",
        description="Null = no override (inherit tenant default). 0 = no auto-expire.",
    )
    "Null = no override (inherit tenant default). 0 = no auto-expire."
    clip_retention_days_override: Optional[int] = Field(
        alias="clipRetentionDaysOverride"
    )


class ClientQoeSummaryDefault(BaseModel):
    """Pre-aggregated client QoE summary from client_qoe_5m."""

    avg_packet_loss_rate: Optional[float] = Field(alias="avgPacketLossRate")
    peak_packet_loss_rate: Optional[float] = Field(alias="peakPacketLossRate")
    avg_bandwidth_in: float = Field(alias="avgBandwidthIn")
    avg_bandwidth_out: float = Field(alias="avgBandwidthOut")
    avg_connection_time: float = Field(alias="avgConnectionTime")
    total_active_sessions: int = Field(alias="totalActiveSessions")


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
    allowed_origins: list[str] = Field(
        alias="allowedOrigins",
        description="Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied.",
    )
    "Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied."


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
    context: Optional[Any] = Field(
        description="Your JSON object, sent as `context` in every access request to the URL. Null when unset."
    )
    "Your JSON object, sent as `context` in every access request to the URL. Null when unset."


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


class ClipInNodeDefault(BaseModel):
    """A video clip extracted from a live stream's DVR buffer.
    Clips are created from recorded stream segments and stored for playback."""

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
    source_stream_id: str = Field(
        alias="sourceStreamId",
        description="Stable UUID of the source stream this clip was created from.",
    )
    "Stable UUID of the source stream this clip was created from."
    stream: Optional["ClipInNodeDefaultStream"]
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
    clip_node_id: Optional[str] = Field(
        alias="clipNodeId", description="Node that processed this clip."
    )
    "Node that processed this clip."
    storage_path: Optional[str] = Field(
        alias="storagePath", description="Storage path on the processing node."
    )
    "Storage path on the processing node."
    size_bytes: Optional[float] = Field(
        alias="sizeBytes", description="File size in bytes."
    )
    "File size in bytes."
    status: str = Field(
        description="Processing status (queued, processing, ready, failed)."
    )
    "Processing status (queued, processing, ready, failed)."
    clip_created_at: Optional[datetime] = Field(
        alias="clipCreatedAt", description="When the clip was requested."
    )
    "When the clip was requested."
    updated_at: Optional[datetime] = Field(
        alias="updatedAt", description="When the clip was last updated."
    )
    "When the clip was last updated."
    clip_mode: Optional[str] = Field(
        alias="clipMode",
        description="Clip creation mode (ABSOLUTE, RELATIVE, DURATION, CLIP_NOW).",
    )
    "Clip creation mode (ABSOLUTE, RELATIVE, DURATION, CLIP_NOW)."
    requested_params: Optional[Any] = Field(
        alias="requestedParams",
        description="Original request parameters for audit/display.",
    )
    "Original request parameters for audit/display."
    storage_location: Optional[str] = Field(
        alias="storageLocation", description="Storage backend (local, s3)."
    )
    "Storage backend (local, s3)."
    sync_status: Optional[str] = Field(
        alias="syncStatus",
        description="Current S3 sync state (pending, in_progress, synced, failed, lost_local).",
    )
    "Current S3 sync state (pending, in_progress, synced, failed, lost_local)."
    has_local_copy: Optional[bool] = Field(
        alias="hasLocalCopy",
        description="Present full local node copy (origin or cache): true when at least one node holds a complete local copy, false when none remain (playback via read-through relay from S3). Null when the placement overlay (Periscope) is unavailable — unknown, not 'no local copy'. Durable S3-only state is derived by consumers as isSynced && hasLocalCopy == false.",
    )
    "Present full local node copy (origin or cache): true when at least one node holds a complete local copy, false when none remain (playback via read-through relay from S3). Null when the placement overlay (Periscope) is unavailable — unknown, not 'no local copy'. Durable S3-only state is derived by consumers as isSynced && hasLocalCopy == false."
    is_synced: Optional[bool] = Field(
        alias="isSynced", description="True when S3 has an authoritative copy."
    )
    "True when S3 has an authoritative copy."
    is_finalized: Optional[bool] = Field(
        alias="isFinalized",
        description="True when the S3 copy includes the Mist .dtsh index.",
    )
    "True when the S3 copy includes the Mist .dtsh index."
    expires_at: Optional[datetime] = Field(
        alias="expiresAt", description="When the clip will be auto-deleted."
    )
    "When the clip will be auto-deleted."
    is_expired: bool = Field(
        alias="isExpired",
        description="Whether the clip has passed its retention date (expiresAt < now).",
    )
    "Whether the clip has passed its retention date (expiresAt < now)."
    playback_policy: Optional["ClipInNodeDefaultPlaybackPolicy"] = Field(
        alias="playbackPolicy",
        description="Playback access policy snapshotted at clip creation. null/PUBLIC means\nanyone with the playbackId can watch. Independent from the source stream's\npolicy after creation — flipping the source stream's policy does not affect\nalready-shared clip URLs.",
    )
    "Playback access policy snapshotted at clip creation. null/PUBLIC means\nanyone with the playbackId can watch. Independent from the source stream's\npolicy after creation — flipping the source stream's policy does not affect\nalready-shared clip URLs."
    thumbnail_assets: Optional["ClipInNodeDefaultThumbnailAssets"] = Field(
        alias="thumbnailAssets",
        description="Server-resolved Chandler URLs for the clip's poster and sprite thumbnails.\nNull until Foghorn confirms the thumbnail upload.",
    )
    "Server-resolved Chandler URLs for the clip's poster and sprite thumbnails.\nNull until Foghorn confirms the thumbnail upload."
    effective_retention: Optional["ClipInNodeDefaultEffectiveRetention"] = Field(
        alias="effectiveRetention",
        description="Resolved retention horizon with the source of the decision (per-asset\noverride → per-stream override → tenant default → tier entitlement).\nNull when retention_until is unset (infinite).",
    )
    "Resolved retention horizon with the source of the decision (per-asset\noverride → per-stream override → tenant default → tier entitlement).\nNull when retention_until is unset (infinite)."
    storage_cost: Optional["ClipInNodeDefaultStorageCost"] = Field(
        alias="storageCost",
        description="Marginal storage cost for this clip on the tenant's tier. Null when\nthe tenant has no storage meter (self-hosted, fully tenant-private\ncluster). Computed from sizeBytes in GiB × the tier's price per GiB-month.",
    )
    "Marginal storage cost for this clip on the tenant's tier. Null when\nthe tenant has no storage meter (self-hosted, fully tenant-private\ncluster). Computed from sizeBytes in GiB × the tier's price per GiB-month."


class ClipInNodeDefaultStream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

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
        description="Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path.",
    )
    "Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path."
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
    pull_source: Optional["ClipInNodeDefaultStreamPullSource"] = Field(
        alias="pullSource",
        description="Pull-source config for pull streams; null for push streams.",
    )
    "Pull-source config for pull streams; null for push streams."
    managed_source: Optional["ClipInNodeDefaultStreamManagedSource"] = Field(
        alias="managedSource",
        description="Safe source summary for managed streams; null for push and pull streams.",
    )
    "Safe source summary for managed streams; null for push and pull streams."
    source_location: "ClipInNodeDefaultStreamSourceLocation" = Field(
        alias="sourceLocation",
        description="Where the stream's source may be ingested, derived from the stream's own ingest placement rules.",
    )
    "Where the stream's source may be ingested, derived from the stream's own ingest placement rules."
    created_at: datetime = Field(
        alias="createdAt", description="When this stream was created."
    )
    "When this stream was created."
    updated_at: datetime = Field(
        alias="updatedAt", description="When this stream was last modified."
    )
    "When this stream was last modified."
    metrics: Optional["ClipInNodeDefaultStreamMetrics"] = Field(
        description="Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    )
    "Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    push_targets: list["ClipInNodeDefaultStreamPushTargets"] = Field(
        alias="pushTargets",
        description="Configured multistream push targets for this stream.",
    )
    "Configured multistream push targets for this stream."
    playback_policy: Optional["ClipInNodeDefaultStreamPlaybackPolicy"] = Field(
        alias="playbackPolicy",
        description="Playback access policy. null/PUBLIC = anyone with the playbackId can watch.",
    )
    "Playback access policy. null/PUBLIC = anyone with the playbackId can watch."
    dvr_chapter_mode: Optional[DVRChapterMode] = Field(
        alias="dvrChapterMode",
        description="How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast.",
    )
    "How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast."
    dvr_chapter_interval_seconds: Optional[int] = Field(
        alias="dvrChapterIntervalSeconds",
        description="Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour).",
    )
    "Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour)."
    monitoring: MonitoringToggle = Field(
        description="Per-stream Skipper monitoring override (INHERIT follows tier)."
    )
    "Per-stream Skipper monitoring override (INHERIT follows tier)."
    thumbnail_assets: Optional["ClipInNodeDefaultStreamThumbnailAssets"] = Field(
        alias="thumbnailAssets",
        description="Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles.",
    )
    "Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles."
    retention_overrides: Optional["ClipInNodeDefaultStreamRetentionOverrides"] = Field(
        alias="retentionOverrides",
        description="Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here.",
    )
    "Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here."


class ClipInNodeDefaultStreamPullSource(BaseModel):
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


class ClipInNodeDefaultStreamManagedSource(BaseModel):
    """Safe summary of an operator-managed source. Literal paths, commands, and
    credentials are intentionally not exposed through the tenant API."""

    source_kind: str = Field(
        alias="sourceKind", description="Managed source form: file, playlist, or exec."
    )
    "Managed source form: file, playlist, or exec."
    always_on: bool = Field(
        alias="alwaysOn",
        description="Whether the source is kept active without waiting for a viewer.",
    )
    "Whether the source is kept active without waiting for a viewer."
    placement_count: int = Field(
        alias="placementCount",
        description="Number of source placements requested by the operator configuration.",
    )
    "Number of source placements requested by the operator configuration."


class ClipInNodeDefaultStreamSourceLocation(BaseModel):
    mode: SourceLocationMode
    avoid_node_ids: list[str] = Field(
        alias="avoidNodeIds", description="Empty unless mode is RESTRICTED."
    )
    "Empty unless mode is RESTRICTED."


class ClipInNodeDefaultStreamMetrics(BaseModel):
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
    node_id: Optional[str] = Field(
        alias="nodeId", description="ID of the edge node handling this stream."
    )
    "ID of the edge node handling this stream."
    track_count: Optional[int] = Field(
        alias="trackCount", description="Number of quality tracks available."
    )
    "Number of quality tracks available."
    total_inputs: Optional[int] = Field(
        alias="totalInputs", description="Total ingest connections to this stream."
    )
    "Total ingest connections to this stream."
    uploaded_bytes: float = Field(
        alias="uploadedBytes",
        description="Total bytes uploaded (ingested) for current session.",
    )
    "Total bytes uploaded (ingested) for current session."
    downloaded_bytes: float = Field(
        alias="downloadedBytes",
        description="Total bytes downloaded (egress) for current session.",
    )
    "Total bytes downloaded (egress) for current session."
    viewer_seconds: float = Field(
        alias="viewerSeconds", description="Total viewer-seconds accumulated."
    )
    "Total viewer-seconds accumulated."
    packets_sent: Optional[float] = Field(
        alias="packetsSent", description="Total packets sent to viewers."
    )
    "Total packets sent to viewers."
    packets_lost: Optional[float] = Field(
        alias="packetsLost", description="Total packets lost in transit."
    )
    "Total packets lost in transit."
    packets_retransmitted: Optional[float] = Field(
        alias="packetsRetransmitted", description="Total packets retransmitted."
    )
    "Total packets retransmitted."
    buffer_state: Optional[str] = Field(
        alias="bufferState",
        description="Buffer health state (HEALTHY, WARNING, CRITICAL).",
    )
    "Buffer health state (HEALTHY, WARNING, CRITICAL)."
    quality_tier: Optional[str] = Field(
        alias="qualityTier",
        description="Highest quality tier available (4K, 1080p, 720p, etc.).",
    )
    "Highest quality tier available (4K, 1080p, 720p, etc.)."
    primary_width: Optional[int] = Field(
        alias="primaryWidth", description="Primary video track width in pixels."
    )
    "Primary video track width in pixels."
    primary_height: Optional[int] = Field(
        alias="primaryHeight", description="Primary video track height in pixels."
    )
    "Primary video track height in pixels."
    primary_fps: Optional[float] = Field(
        alias="primaryFps", description="Primary video track framerate."
    )
    "Primary video track framerate."
    primary_codec: Optional[str] = Field(
        alias="primaryCodec",
        description="Primary video codec (H.264, H.265, VP9, AV1).",
    )
    "Primary video codec (H.264, H.265, VP9, AV1)."
    primary_bitrate: Optional[int] = Field(
        alias="primaryBitrate", description="Primary video bitrate in kbps."
    )
    "Primary video bitrate in kbps."
    has_issues: Optional[bool] = Field(
        alias="hasIssues", description="Whether the stream has active quality issues."
    )
    "Whether the stream has active quality issues."
    issues_description: Optional[str] = Field(
        alias="issuesDescription",
        description="Human-readable description of current issues.",
    )
    "Human-readable description of current issues."


class ClipInNodeDefaultStreamPushTargets(BaseModel):
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


class ClipInNodeDefaultStreamPlaybackPolicy(BaseModel):
    """Per-playback-object access policy. Foghorn reads this in the USER_NEW
    trigger handler; the requiresAuth marker (on the playback object itself)
    gates whether the full policy is fetched at all."""

    type_: PlaybackPolicyType = Field(alias="type")
    allowed_origins: list[str] = Field(
        alias="allowedOrigins",
        description="Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied.",
    )
    "Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied."


class ClipInNodeDefaultStreamThumbnailAssets(BaseModel):
    """Chandler-served thumbnail asset URLs (poster, sprite, VTT cues). URL shape
    is `{chandlerBase}/assets/{assetKey}/poster.jpg`,
    `/assets/{assetKey}/sprite.jpg`, `/assets/{assetKey}/sprite.vtt` — Chandler
    serves the object key directly, no version resolution. assetKey is stream_id
    for live streams; clip_hash / dvr_hash / vod_hash (= artifact_hash) for artifacts."""

    poster_url: str = Field(alias="posterUrl")
    sprite_vtt_url: str = Field(alias="spriteVttUrl")
    sprite_jpg_url: str = Field(alias="spriteJpgUrl")
    asset_key: str = Field(alias="assetKey")


class ClipInNodeDefaultStreamRetentionOverrides(BaseModel):
    """Per-stream retention overrides for DVR and clips. VOD uploads aren't
    stream-bound, so they have no stream-level knob here."""

    stream_id: str = Field(alias="streamId")
    dvr_retention_days_override: Optional[int] = Field(
        alias="dvrRetentionDaysOverride",
        description="Null = no override (inherit tenant default). 0 = no auto-expire.",
    )
    "Null = no override (inherit tenant default). 0 = no auto-expire."
    clip_retention_days_override: Optional[int] = Field(
        alias="clipRetentionDaysOverride"
    )


class ClipInNodeDefaultPlaybackPolicy(BaseModel):
    """Per-playback-object access policy. Foghorn reads this in the USER_NEW
    trigger handler; the requiresAuth marker (on the playback object itself)
    gates whether the full policy is fetched at all."""

    type_: PlaybackPolicyType = Field(alias="type")
    jwt: Optional["ClipInNodeDefaultPlaybackPolicyJwt"] = Field(
        description="JWT-policy details, populated when type == JWT."
    )
    "JWT-policy details, populated when type == JWT."
    webhook: Optional["ClipInNodeDefaultPlaybackPolicyWebhook"] = Field(
        description="Webhook-policy details, populated when type == WEBHOOK. Secret is masked."
    )
    "Webhook-policy details, populated when type == WEBHOOK. Secret is masked."
    allowed_origins: list[str] = Field(
        alias="allowedOrigins",
        description="Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied.",
    )
    "Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied."


class ClipInNodeDefaultPlaybackPolicyJwt(BaseModel):
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


class ClipInNodeDefaultPlaybackPolicyWebhook(BaseModel):
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
    context: Optional[Any] = Field(
        description="Your JSON object, sent as `context` in every access request to the URL. Null when unset."
    )
    "Your JSON object, sent as `context` in every access request to the URL. Null when unset."


class ClipInNodeDefaultThumbnailAssets(BaseModel):
    """Chandler-served thumbnail asset URLs (poster, sprite, VTT cues). URL shape
    is `{chandlerBase}/assets/{assetKey}/poster.jpg`,
    `/assets/{assetKey}/sprite.jpg`, `/assets/{assetKey}/sprite.vtt` — Chandler
    serves the object key directly, no version resolution. assetKey is stream_id
    for live streams; clip_hash / dvr_hash / vod_hash (= artifact_hash) for artifacts."""

    poster_url: str = Field(alias="posterUrl")
    sprite_vtt_url: str = Field(alias="spriteVttUrl")
    sprite_jpg_url: str = Field(alias="spriteJpgUrl")
    asset_key: str = Field(alias="assetKey")


class ClipInNodeDefaultEffectiveRetention(BaseModel):
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


class ClipInNodeDefaultStorageCost(BaseModel):
    """Marginal per-asset storage cost projection. Used by the customer-facing
    storage browser to show "this clip costs you ~$0.01/day". The unit is
    the tier's currency (typically EUR). Rating prices storage per GiB-month
    (a fixed 730-hour month); perMonth = GiB × price, perDay = perMonth × 24 / 730."""

    per_day: float = Field(alias="perDay")
    per_month: float = Field(alias="perMonth")
    currency: str


class ClusterAccessDefault(BaseModel):
    cluster_id: str = Field(alias="clusterId")
    cluster_name: str = Field(alias="clusterName")
    access_level: str = Field(alias="accessLevel")
    resource_limits: Optional[Any] = Field(alias="resourceLimits")
    allow_private_pull_sources: bool = Field(
        alias="allowPrivatePullSources",
        description="Whether the cluster may pull from private (RFC 1918) and multicast sources.",
    )
    "Whether the cluster may pull from private (RFC 1918) and multicast sources."


class ClusterBootOpsDefault(BaseModel):
    """Cluster-ops boot aggregate for operators of the serving cluster. Aggregate and
    redacted: never exposes content/stream/session/URL/tenant identifiers. Populated
    only from token-attributed rows."""

    serving_cluster_id: str = Field(alias="servingClusterId")
    node_id: str = Field(alias="nodeId")
    protocol: str
    boot_count: int = Field(alias="bootCount")
    error_count: int = Field(alias="errorCount")
    p_95_ttf_ms: float = Field(alias="p95TtfMs")
    cache_hit_ratio: float = Field(alias="cacheHitRatio")


class ClusterDefault(BaseModel):
    """A streaming cluster containing one or more infrastructure nodes.
    Clusters provide isolated capacity for streaming workloads."""

    id: str = Field(description="Global unique identifier for Relay compatibility.")
    "Global unique identifier for Relay compatibility."
    cluster_id: str = Field(
        alias="clusterId", description="Internal cluster identifier (UUID)."
    )
    "Internal cluster identifier (UUID)."
    cluster_name: str = Field(
        alias="clusterName", description="Human-readable cluster name."
    )
    "Human-readable cluster name."
    cluster_type: str = Field(
        alias="clusterType",
        description="Cluster architecture type (dedicated, shared, hybrid).",
    )
    "Cluster architecture type (dedicated, shared, hybrid)."
    deployment_model: str = Field(
        alias="deploymentModel",
        description="Deployment model (platform, private, hybrid).",
    )
    "Deployment model (platform, private, hybrid)."
    base_url: str = Field(
        alias="baseUrl", description="Base URL for cluster API access."
    )
    "Base URL for cluster API access."
    database_url: Optional[str] = Field(
        alias="databaseUrl", description="Internal database connection URL."
    )
    "Internal database connection URL."
    periscope_url: Optional[str] = Field(
        alias="periscopeUrl", description="Analytics query endpoint URL."
    )
    "Analytics query endpoint URL."
    kafka_brokers: Optional[list[str]] = Field(
        alias="kafkaBrokers", description="Kafka broker addresses for event streaming."
    )
    "Kafka broker addresses for event streaming."
    max_concurrent_streams: int = Field(
        alias="maxConcurrentStreams", description="Maximum allowed concurrent streams."
    )
    "Maximum allowed concurrent streams."
    max_concurrent_viewers: int = Field(
        alias="maxConcurrentViewers", description="Maximum allowed concurrent viewers."
    )
    "Maximum allowed concurrent viewers."
    max_bandwidth_mbps: int = Field(
        alias="maxBandwidthMbps", description="Maximum allowed bandwidth in Mbps."
    )
    "Maximum allowed bandwidth in Mbps."
    health_status: str = Field(
        alias="healthStatus",
        description="Cluster health status (healthy, degraded, critical).",
    )
    "Cluster health status (healthy, degraded, critical)."
    is_active: bool = Field(
        alias="isActive", description="Whether the cluster is operational."
    )
    "Whether the cluster is operational."
    is_default_cluster: bool = Field(
        alias="isDefaultCluster",
        description="Whether this is the tenant's default cluster.",
    )
    "Whether this is the tenant's default cluster."
    is_platform_official: bool = Field(
        alias="isPlatformOfficial",
        description="Whether this cluster is a platform-official regional/control cell.",
    )
    "Whether this cluster is a platform-official regional/control cell."
    region_id: Optional[str] = Field(
        alias="regionId", description="Operational region identifier for this cluster."
    )
    "Operational region identifier for this cluster."
    is_subscribed: bool = Field(
        alias="isSubscribed", description="Whether the current tenant is subscribed."
    )
    "Whether the current tenant is subscribed."
    created_at: Optional[datetime] = Field(
        alias="createdAt", description="When the cluster was created."
    )
    "When the cluster was created."
    updated_at: Optional[datetime] = Field(
        alias="updatedAt", description="When the cluster was last updated."
    )
    "When the cluster was last updated."
    owner_tenant_id: Optional[str] = Field(
        alias="ownerTenantId",
        description="Tenant that owns this cluster (for private clusters).",
    )
    "Tenant that owns this cluster (for private clusters)."
    visibility: ClusterVisibility = Field(
        description="Marketplace visibility (PUBLIC, PRIVATE, INVITE_ONLY)."
    )
    "Marketplace visibility (PUBLIC, PRIVATE, INVITE_ONLY)."
    pricing_model: ClusterPricingModel = Field(
        alias="pricingModel", description="Pricing model for subscriptions."
    )
    "Pricing model for subscriptions."
    monthly_price_cents: Optional[int] = Field(
        alias="monthlyPriceCents", description="Monthly subscription price in cents."
    )
    "Monthly subscription price in cents."
    requires_approval: bool = Field(
        alias="requiresApproval", description="Whether access requires owner approval."
    )
    "Whether access requires owner approval."
    short_description: Optional[str] = Field(
        alias="shortDescription", description="Short marketplace description."
    )
    "Short marketplace description."


class ClusterInNodeDefault(BaseModel):
    """A streaming cluster containing one or more infrastructure nodes.
    Clusters provide isolated capacity for streaming workloads."""

    id: str = Field(description="Global unique identifier for Relay compatibility.")
    "Global unique identifier for Relay compatibility."
    cluster_cluster_id: str = Field(
        alias="clusterClusterId", description="Internal cluster identifier (UUID)."
    )
    "Internal cluster identifier (UUID)."
    cluster_name: str = Field(
        alias="clusterName", description="Human-readable cluster name."
    )
    "Human-readable cluster name."
    cluster_type: str = Field(
        alias="clusterType",
        description="Cluster architecture type (dedicated, shared, hybrid).",
    )
    "Cluster architecture type (dedicated, shared, hybrid)."
    deployment_model: str = Field(
        alias="deploymentModel",
        description="Deployment model (platform, private, hybrid).",
    )
    "Deployment model (platform, private, hybrid)."
    base_url: str = Field(
        alias="baseUrl", description="Base URL for cluster API access."
    )
    "Base URL for cluster API access."
    database_url: Optional[str] = Field(
        alias="databaseUrl", description="Internal database connection URL."
    )
    "Internal database connection URL."
    periscope_url: Optional[str] = Field(
        alias="periscopeUrl", description="Analytics query endpoint URL."
    )
    "Analytics query endpoint URL."
    kafka_brokers: Optional[list[str]] = Field(
        alias="kafkaBrokers", description="Kafka broker addresses for event streaming."
    )
    "Kafka broker addresses for event streaming."
    max_concurrent_streams: int = Field(
        alias="maxConcurrentStreams", description="Maximum allowed concurrent streams."
    )
    "Maximum allowed concurrent streams."
    max_concurrent_viewers: int = Field(
        alias="maxConcurrentViewers", description="Maximum allowed concurrent viewers."
    )
    "Maximum allowed concurrent viewers."
    max_bandwidth_mbps: int = Field(
        alias="maxBandwidthMbps", description="Maximum allowed bandwidth in Mbps."
    )
    "Maximum allowed bandwidth in Mbps."
    health_status: str = Field(
        alias="healthStatus",
        description="Cluster health status (healthy, degraded, critical).",
    )
    "Cluster health status (healthy, degraded, critical)."
    is_active: bool = Field(
        alias="isActive", description="Whether the cluster is operational."
    )
    "Whether the cluster is operational."
    is_default_cluster: bool = Field(
        alias="isDefaultCluster",
        description="Whether this is the tenant's default cluster.",
    )
    "Whether this is the tenant's default cluster."
    is_platform_official: bool = Field(
        alias="isPlatformOfficial",
        description="Whether this cluster is a platform-official regional/control cell.",
    )
    "Whether this cluster is a platform-official regional/control cell."
    region_id: Optional[str] = Field(
        alias="regionId", description="Operational region identifier for this cluster."
    )
    "Operational region identifier for this cluster."
    is_subscribed: bool = Field(
        alias="isSubscribed", description="Whether the current tenant is subscribed."
    )
    "Whether the current tenant is subscribed."
    cluster_created_at: Optional[datetime] = Field(
        alias="clusterCreatedAt", description="When the cluster was created."
    )
    "When the cluster was created."
    updated_at: Optional[datetime] = Field(
        alias="updatedAt", description="When the cluster was last updated."
    )
    "When the cluster was last updated."
    owner_tenant_id: Optional[str] = Field(
        alias="ownerTenantId",
        description="Tenant that owns this cluster (for private clusters).",
    )
    "Tenant that owns this cluster (for private clusters)."
    visibility: ClusterVisibility = Field(
        description="Marketplace visibility (PUBLIC, PRIVATE, INVITE_ONLY)."
    )
    "Marketplace visibility (PUBLIC, PRIVATE, INVITE_ONLY)."
    pricing_model: ClusterPricingModel = Field(
        alias="pricingModel", description="Pricing model for subscriptions."
    )
    "Pricing model for subscriptions."
    monthly_price_cents: Optional[int] = Field(
        alias="monthlyPriceCents", description="Monthly subscription price in cents."
    )
    "Monthly subscription price in cents."
    requires_approval: bool = Field(
        alias="requiresApproval", description="Whether access requires owner approval."
    )
    "Whether access requires owner approval."
    short_description: Optional[str] = Field(
        alias="shortDescription", description="Short marketplace description."
    )
    "Short marketplace description."


class ClusterInviteDefault(BaseModel):
    id: str
    cluster_id: str = Field(alias="clusterId")
    invited_tenant_id: str = Field(alias="invitedTenantId")
    invite_token: Optional[str] = Field(alias="inviteToken")
    access_level: str = Field(alias="accessLevel")
    resource_limits: Optional[Any] = Field(alias="resourceLimits")
    status: str
    created_by: str = Field(alias="createdBy")
    created_at: datetime = Field(alias="createdAt")
    expires_at: Optional[datetime] = Field(alias="expiresAt")
    accepted_at: Optional[datetime] = Field(alias="acceptedAt")
    invited_tenant_name: Optional[str] = Field(alias="invitedTenantName")
    cluster_name: Optional[str] = Field(alias="clusterName")


class ClusterPairTrafficDefault(BaseModel):
    cluster_id: str = Field(alias="clusterId")
    remote_cluster_id: str = Field(alias="remoteClusterId")
    event_count: int = Field(alias="eventCount")
    success_count: int = Field(alias="successCount")
    avg_latency_ms: float = Field(alias="avgLatencyMs")
    avg_distance_km: float = Field(alias="avgDistanceKm")
    success_rate: float = Field(alias="successRate")
    max_latency_ms: float = Field(alias="maxLatencyMs")
    local_latitude: Optional[float] = Field(alias="localLatitude")
    local_longitude: Optional[float] = Field(alias="localLongitude")
    remote_latitude: Optional[float] = Field(alias="remoteLatitude")
    remote_longitude: Optional[float] = Field(alias="remoteLongitude")


class ClusterQoeOpsDefault(BaseModel):
    """Cluster-ops viewer-QoE aggregate per serving node/protocol (token-attributed,
    redacted — no content/stream/session identifiers)."""

    serving_cluster_id: str = Field(alias="servingClusterId")
    node_id: str = Field(alias="nodeId")
    protocol: str
    session_count: int = Field(alias="sessionCount")
    rebuffering_ratio: float = Field(alias="rebufferingRatio")
    frame_drop_ratio: float = Field(alias="frameDropRatio")
    avg_bitrate_bps: float = Field(alias="avgBitrateBps")


class ClusterSubscriptionDefault(BaseModel):
    id: str
    tenant_id: str = Field(alias="tenantId")
    cluster_id: str = Field(alias="clusterId")
    access_level: str = Field(alias="accessLevel")
    subscription_status: ClusterSubscriptionStatus = Field(alias="subscriptionStatus")
    resource_limits: Optional[Any] = Field(alias="resourceLimits")
    requested_at: Optional[datetime] = Field(alias="requestedAt")
    approved_at: Optional[datetime] = Field(alias="approvedAt")
    approved_by: Optional[str] = Field(alias="approvedBy")
    rejection_reason: Optional[str] = Field(alias="rejectionReason")
    expires_at: Optional[datetime] = Field(alias="expiresAt")
    created_at: datetime = Field(alias="createdAt")
    updated_at: datetime = Field(alias="updatedAt")
    cluster_name: Optional[str] = Field(alias="clusterName")
    tenant_name: Optional[str] = Field(alias="tenantName")


class ClusterWorkloadDefault(BaseModel):
    cluster_id: str = Field(alias="clusterId")
    node_id: str = Field(alias="nodeId")
    work_kind: str = Field(alias="workKind")
    measurement_kind: str = Field(
        alias="measurementKind",
        description="Whether metrics describe the requested window or a freshness-bounded current observation.",
    )
    "Whether metrics describe the requested window or a freshness-bounded current observation."
    storage_scope: Optional[str] = Field(
        alias="storageScope",
        description="Storage residency scope for current storage observations (`hot` or `cold`).",
    )
    "Storage residency scope for current storage observations (`hot` or `cold`)."
    observed_at: Optional[datetime] = Field(
        alias="observedAt", description="Source observation time for current rows."
    )
    "Source observation time for current rows."
    event_count: int = Field(alias="eventCount")
    active_count: int = Field(alias="activeCount")
    bytes_: float = Field(alias="bytes")
    media_seconds: float = Field(alias="mediaSeconds")
    error_count: int = Field(alias="errorCount")


class ConnectionEventDefault(BaseModel):
    id: str
    event_id: str = Field(alias="eventId")
    timestamp: datetime
    stream_id: str = Field(alias="streamId")
    stream: Optional["ConnectionEventDefaultStream"]
    session_id: str = Field(alias="sessionId")
    connection_addr: Optional[str] = Field(alias="connectionAddr")
    connector: str
    node_id: str = Field(alias="nodeId")
    country_code: Optional[str] = Field(alias="countryCode")
    city: Optional[str]
    latitude: Optional[float]
    longitude: Optional[float]
    client_bucket: Optional["ConnectionEventDefaultClientBucket"] = Field(
        alias="clientBucket"
    )
    node_bucket: Optional["ConnectionEventDefaultNodeBucket"] = Field(
        alias="nodeBucket"
    )
    event_type: str = Field(alias="eventType")
    request_url: Optional[str] = Field(alias="requestUrl")
    cluster_id: str = Field(alias="clusterId")
    origin_cluster_id: Optional[str] = Field(alias="originClusterId")
    control_cell_id: Optional[str] = Field(alias="controlCellId")
    session_duration_seconds: Optional[int] = Field(alias="sessionDurationSeconds")
    bytes_transferred: Optional[float] = Field(alias="bytesTransferred")


class ConnectionEventDefaultStream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

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
        description="Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path.",
    )
    "Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path."
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
    pull_source: Optional["ConnectionEventDefaultStreamPullSource"] = Field(
        alias="pullSource",
        description="Pull-source config for pull streams; null for push streams.",
    )
    "Pull-source config for pull streams; null for push streams."
    managed_source: Optional["ConnectionEventDefaultStreamManagedSource"] = Field(
        alias="managedSource",
        description="Safe source summary for managed streams; null for push and pull streams.",
    )
    "Safe source summary for managed streams; null for push and pull streams."
    source_location: "ConnectionEventDefaultStreamSourceLocation" = Field(
        alias="sourceLocation",
        description="Where the stream's source may be ingested, derived from the stream's own ingest placement rules.",
    )
    "Where the stream's source may be ingested, derived from the stream's own ingest placement rules."
    created_at: datetime = Field(
        alias="createdAt", description="When this stream was created."
    )
    "When this stream was created."
    updated_at: datetime = Field(
        alias="updatedAt", description="When this stream was last modified."
    )
    "When this stream was last modified."
    metrics: Optional["ConnectionEventDefaultStreamMetrics"] = Field(
        description="Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    )
    "Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    push_targets: list["ConnectionEventDefaultStreamPushTargets"] = Field(
        alias="pushTargets",
        description="Configured multistream push targets for this stream.",
    )
    "Configured multistream push targets for this stream."
    playback_policy: Optional["ConnectionEventDefaultStreamPlaybackPolicy"] = Field(
        alias="playbackPolicy",
        description="Playback access policy. null/PUBLIC = anyone with the playbackId can watch.",
    )
    "Playback access policy. null/PUBLIC = anyone with the playbackId can watch."
    dvr_chapter_mode: Optional[DVRChapterMode] = Field(
        alias="dvrChapterMode",
        description="How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast.",
    )
    "How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast."
    dvr_chapter_interval_seconds: Optional[int] = Field(
        alias="dvrChapterIntervalSeconds",
        description="Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour).",
    )
    "Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour)."
    monitoring: MonitoringToggle = Field(
        description="Per-stream Skipper monitoring override (INHERIT follows tier)."
    )
    "Per-stream Skipper monitoring override (INHERIT follows tier)."
    thumbnail_assets: Optional["ConnectionEventDefaultStreamThumbnailAssets"] = Field(
        alias="thumbnailAssets",
        description="Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles.",
    )
    "Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles."
    retention_overrides: Optional["ConnectionEventDefaultStreamRetentionOverrides"] = (
        Field(
            alias="retentionOverrides",
            description="Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here.",
        )
    )
    "Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here."


class ConnectionEventDefaultStreamPullSource(BaseModel):
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


class ConnectionEventDefaultStreamManagedSource(BaseModel):
    """Safe summary of an operator-managed source. Literal paths, commands, and
    credentials are intentionally not exposed through the tenant API."""

    source_kind: str = Field(
        alias="sourceKind", description="Managed source form: file, playlist, or exec."
    )
    "Managed source form: file, playlist, or exec."
    always_on: bool = Field(
        alias="alwaysOn",
        description="Whether the source is kept active without waiting for a viewer.",
    )
    "Whether the source is kept active without waiting for a viewer."
    placement_count: int = Field(
        alias="placementCount",
        description="Number of source placements requested by the operator configuration.",
    )
    "Number of source placements requested by the operator configuration."


class ConnectionEventDefaultStreamSourceLocation(BaseModel):
    mode: SourceLocationMode
    avoid_node_ids: list[str] = Field(
        alias="avoidNodeIds", description="Empty unless mode is RESTRICTED."
    )
    "Empty unless mode is RESTRICTED."


class ConnectionEventDefaultStreamMetrics(BaseModel):
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
    node_id: Optional[str] = Field(
        alias="nodeId", description="ID of the edge node handling this stream."
    )
    "ID of the edge node handling this stream."
    track_count: Optional[int] = Field(
        alias="trackCount", description="Number of quality tracks available."
    )
    "Number of quality tracks available."
    total_inputs: Optional[int] = Field(
        alias="totalInputs", description="Total ingest connections to this stream."
    )
    "Total ingest connections to this stream."
    uploaded_bytes: float = Field(
        alias="uploadedBytes",
        description="Total bytes uploaded (ingested) for current session.",
    )
    "Total bytes uploaded (ingested) for current session."
    downloaded_bytes: float = Field(
        alias="downloadedBytes",
        description="Total bytes downloaded (egress) for current session.",
    )
    "Total bytes downloaded (egress) for current session."
    viewer_seconds: float = Field(
        alias="viewerSeconds", description="Total viewer-seconds accumulated."
    )
    "Total viewer-seconds accumulated."
    packets_sent: Optional[float] = Field(
        alias="packetsSent", description="Total packets sent to viewers."
    )
    "Total packets sent to viewers."
    packets_lost: Optional[float] = Field(
        alias="packetsLost", description="Total packets lost in transit."
    )
    "Total packets lost in transit."
    packets_retransmitted: Optional[float] = Field(
        alias="packetsRetransmitted", description="Total packets retransmitted."
    )
    "Total packets retransmitted."
    buffer_state: Optional[str] = Field(
        alias="bufferState",
        description="Buffer health state (HEALTHY, WARNING, CRITICAL).",
    )
    "Buffer health state (HEALTHY, WARNING, CRITICAL)."
    quality_tier: Optional[str] = Field(
        alias="qualityTier",
        description="Highest quality tier available (4K, 1080p, 720p, etc.).",
    )
    "Highest quality tier available (4K, 1080p, 720p, etc.)."
    primary_width: Optional[int] = Field(
        alias="primaryWidth", description="Primary video track width in pixels."
    )
    "Primary video track width in pixels."
    primary_height: Optional[int] = Field(
        alias="primaryHeight", description="Primary video track height in pixels."
    )
    "Primary video track height in pixels."
    primary_fps: Optional[float] = Field(
        alias="primaryFps", description="Primary video track framerate."
    )
    "Primary video track framerate."
    primary_codec: Optional[str] = Field(
        alias="primaryCodec",
        description="Primary video codec (H.264, H.265, VP9, AV1).",
    )
    "Primary video codec (H.264, H.265, VP9, AV1)."
    primary_bitrate: Optional[int] = Field(
        alias="primaryBitrate", description="Primary video bitrate in kbps."
    )
    "Primary video bitrate in kbps."
    has_issues: Optional[bool] = Field(
        alias="hasIssues", description="Whether the stream has active quality issues."
    )
    "Whether the stream has active quality issues."
    issues_description: Optional[str] = Field(
        alias="issuesDescription",
        description="Human-readable description of current issues.",
    )
    "Human-readable description of current issues."


class ConnectionEventDefaultStreamPushTargets(BaseModel):
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


class ConnectionEventDefaultStreamPlaybackPolicy(BaseModel):
    """Per-playback-object access policy. Foghorn reads this in the USER_NEW
    trigger handler; the requiresAuth marker (on the playback object itself)
    gates whether the full policy is fetched at all."""

    type_: PlaybackPolicyType = Field(alias="type")
    allowed_origins: list[str] = Field(
        alias="allowedOrigins",
        description="Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied.",
    )
    "Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied."


class ConnectionEventDefaultStreamThumbnailAssets(BaseModel):
    """Chandler-served thumbnail asset URLs (poster, sprite, VTT cues). URL shape
    is `{chandlerBase}/assets/{assetKey}/poster.jpg`,
    `/assets/{assetKey}/sprite.jpg`, `/assets/{assetKey}/sprite.vtt` — Chandler
    serves the object key directly, no version resolution. assetKey is stream_id
    for live streams; clip_hash / dvr_hash / vod_hash (= artifact_hash) for artifacts."""

    poster_url: str = Field(alias="posterUrl")
    sprite_vtt_url: str = Field(alias="spriteVttUrl")
    sprite_jpg_url: str = Field(alias="spriteJpgUrl")
    asset_key: str = Field(alias="assetKey")


class ConnectionEventDefaultStreamRetentionOverrides(BaseModel):
    """Per-stream retention overrides for DVR and clips. VOD uploads aren't
    stream-bound, so they have no stream-level knob here."""

    stream_id: str = Field(alias="streamId")
    dvr_retention_days_override: Optional[int] = Field(
        alias="dvrRetentionDaysOverride",
        description="Null = no override (inherit tenant default). 0 = no auto-expire.",
    )
    "Null = no override (inherit tenant default). 0 = no auto-expire."
    clip_retention_days_override: Optional[int] = Field(
        alias="clipRetentionDaysOverride"
    )


class ConnectionEventDefaultClientBucket(BaseModel):
    h_3_index: str = Field(alias="h3Index")
    resolution: int


class ConnectionEventDefaultNodeBucket(BaseModel):
    h_3_index: str = Field(alias="h3Index")
    resolution: int


class ConnectionEventInNodeDefault(BaseModel):
    id: str
    connection_event_event_id: str = Field(alias="connectionEventEventId")
    timestamp: datetime
    stream_id: str = Field(alias="streamId")
    stream: Optional["ConnectionEventInNodeDefaultStream"]
    session_id: str = Field(alias="sessionId")
    connection_addr: Optional[str] = Field(alias="connectionAddr")
    connector: str
    node_id: str = Field(alias="nodeId")
    country_code: Optional[str] = Field(alias="countryCode")
    city: Optional[str]
    latitude: Optional[float]
    longitude: Optional[float]
    client_bucket: Optional["ConnectionEventInNodeDefaultClientBucket"] = Field(
        alias="clientBucket"
    )
    node_bucket: Optional["ConnectionEventInNodeDefaultNodeBucket"] = Field(
        alias="nodeBucket"
    )
    event_type: str = Field(alias="eventType")
    request_url: Optional[str] = Field(alias="requestUrl")
    connection_event_cluster_id: str = Field(alias="connectionEventClusterId")
    origin_cluster_id: Optional[str] = Field(alias="originClusterId")
    control_cell_id: Optional[str] = Field(alias="controlCellId")
    session_duration_seconds: Optional[int] = Field(alias="sessionDurationSeconds")
    bytes_transferred: Optional[float] = Field(alias="bytesTransferred")


class ConnectionEventInNodeDefaultStream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

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
        description="Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path.",
    )
    "Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path."
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
    pull_source: Optional["ConnectionEventInNodeDefaultStreamPullSource"] = Field(
        alias="pullSource",
        description="Pull-source config for pull streams; null for push streams.",
    )
    "Pull-source config for pull streams; null for push streams."
    managed_source: Optional["ConnectionEventInNodeDefaultStreamManagedSource"] = Field(
        alias="managedSource",
        description="Safe source summary for managed streams; null for push and pull streams.",
    )
    "Safe source summary for managed streams; null for push and pull streams."
    source_location: "ConnectionEventInNodeDefaultStreamSourceLocation" = Field(
        alias="sourceLocation",
        description="Where the stream's source may be ingested, derived from the stream's own ingest placement rules.",
    )
    "Where the stream's source may be ingested, derived from the stream's own ingest placement rules."
    created_at: datetime = Field(
        alias="createdAt", description="When this stream was created."
    )
    "When this stream was created."
    updated_at: datetime = Field(
        alias="updatedAt", description="When this stream was last modified."
    )
    "When this stream was last modified."
    metrics: Optional["ConnectionEventInNodeDefaultStreamMetrics"] = Field(
        description="Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    )
    "Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    push_targets: list["ConnectionEventInNodeDefaultStreamPushTargets"] = Field(
        alias="pushTargets",
        description="Configured multistream push targets for this stream.",
    )
    "Configured multistream push targets for this stream."
    playback_policy: Optional["ConnectionEventInNodeDefaultStreamPlaybackPolicy"] = (
        Field(
            alias="playbackPolicy",
            description="Playback access policy. null/PUBLIC = anyone with the playbackId can watch.",
        )
    )
    "Playback access policy. null/PUBLIC = anyone with the playbackId can watch."
    dvr_chapter_mode: Optional[DVRChapterMode] = Field(
        alias="dvrChapterMode",
        description="How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast.",
    )
    "How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast."
    dvr_chapter_interval_seconds: Optional[int] = Field(
        alias="dvrChapterIntervalSeconds",
        description="Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour).",
    )
    "Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour)."
    monitoring: MonitoringToggle = Field(
        description="Per-stream Skipper monitoring override (INHERIT follows tier)."
    )
    "Per-stream Skipper monitoring override (INHERIT follows tier)."
    thumbnail_assets: Optional["ConnectionEventInNodeDefaultStreamThumbnailAssets"] = (
        Field(
            alias="thumbnailAssets",
            description="Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles.",
        )
    )
    "Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles."
    retention_overrides: Optional[
        "ConnectionEventInNodeDefaultStreamRetentionOverrides"
    ] = Field(
        alias="retentionOverrides",
        description="Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here.",
    )
    "Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here."


class ConnectionEventInNodeDefaultStreamPullSource(BaseModel):
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


class ConnectionEventInNodeDefaultStreamManagedSource(BaseModel):
    """Safe summary of an operator-managed source. Literal paths, commands, and
    credentials are intentionally not exposed through the tenant API."""

    source_kind: str = Field(
        alias="sourceKind", description="Managed source form: file, playlist, or exec."
    )
    "Managed source form: file, playlist, or exec."
    always_on: bool = Field(
        alias="alwaysOn",
        description="Whether the source is kept active without waiting for a viewer.",
    )
    "Whether the source is kept active without waiting for a viewer."
    placement_count: int = Field(
        alias="placementCount",
        description="Number of source placements requested by the operator configuration.",
    )
    "Number of source placements requested by the operator configuration."


class ConnectionEventInNodeDefaultStreamSourceLocation(BaseModel):
    mode: SourceLocationMode
    avoid_node_ids: list[str] = Field(
        alias="avoidNodeIds", description="Empty unless mode is RESTRICTED."
    )
    "Empty unless mode is RESTRICTED."


class ConnectionEventInNodeDefaultStreamMetrics(BaseModel):
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
    node_id: Optional[str] = Field(
        alias="nodeId", description="ID of the edge node handling this stream."
    )
    "ID of the edge node handling this stream."
    track_count: Optional[int] = Field(
        alias="trackCount", description="Number of quality tracks available."
    )
    "Number of quality tracks available."
    total_inputs: Optional[int] = Field(
        alias="totalInputs", description="Total ingest connections to this stream."
    )
    "Total ingest connections to this stream."
    uploaded_bytes: float = Field(
        alias="uploadedBytes",
        description="Total bytes uploaded (ingested) for current session.",
    )
    "Total bytes uploaded (ingested) for current session."
    downloaded_bytes: float = Field(
        alias="downloadedBytes",
        description="Total bytes downloaded (egress) for current session.",
    )
    "Total bytes downloaded (egress) for current session."
    viewer_seconds: float = Field(
        alias="viewerSeconds", description="Total viewer-seconds accumulated."
    )
    "Total viewer-seconds accumulated."
    packets_sent: Optional[float] = Field(
        alias="packetsSent", description="Total packets sent to viewers."
    )
    "Total packets sent to viewers."
    packets_lost: Optional[float] = Field(
        alias="packetsLost", description="Total packets lost in transit."
    )
    "Total packets lost in transit."
    packets_retransmitted: Optional[float] = Field(
        alias="packetsRetransmitted", description="Total packets retransmitted."
    )
    "Total packets retransmitted."
    buffer_state: Optional[str] = Field(
        alias="bufferState",
        description="Buffer health state (HEALTHY, WARNING, CRITICAL).",
    )
    "Buffer health state (HEALTHY, WARNING, CRITICAL)."
    quality_tier: Optional[str] = Field(
        alias="qualityTier",
        description="Highest quality tier available (4K, 1080p, 720p, etc.).",
    )
    "Highest quality tier available (4K, 1080p, 720p, etc.)."
    primary_width: Optional[int] = Field(
        alias="primaryWidth", description="Primary video track width in pixels."
    )
    "Primary video track width in pixels."
    primary_height: Optional[int] = Field(
        alias="primaryHeight", description="Primary video track height in pixels."
    )
    "Primary video track height in pixels."
    primary_fps: Optional[float] = Field(
        alias="primaryFps", description="Primary video track framerate."
    )
    "Primary video track framerate."
    primary_codec: Optional[str] = Field(
        alias="primaryCodec",
        description="Primary video codec (H.264, H.265, VP9, AV1).",
    )
    "Primary video codec (H.264, H.265, VP9, AV1)."
    primary_bitrate: Optional[int] = Field(
        alias="primaryBitrate", description="Primary video bitrate in kbps."
    )
    "Primary video bitrate in kbps."
    has_issues: Optional[bool] = Field(
        alias="hasIssues", description="Whether the stream has active quality issues."
    )
    "Whether the stream has active quality issues."
    issues_description: Optional[str] = Field(
        alias="issuesDescription",
        description="Human-readable description of current issues.",
    )
    "Human-readable description of current issues."


class ConnectionEventInNodeDefaultStreamPushTargets(BaseModel):
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


class ConnectionEventInNodeDefaultStreamPlaybackPolicy(BaseModel):
    """Per-playback-object access policy. Foghorn reads this in the USER_NEW
    trigger handler; the requiresAuth marker (on the playback object itself)
    gates whether the full policy is fetched at all."""

    type_: PlaybackPolicyType = Field(alias="type")
    allowed_origins: list[str] = Field(
        alias="allowedOrigins",
        description="Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied.",
    )
    "Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied."


class ConnectionEventInNodeDefaultStreamThumbnailAssets(BaseModel):
    """Chandler-served thumbnail asset URLs (poster, sprite, VTT cues). URL shape
    is `{chandlerBase}/assets/{assetKey}/poster.jpg`,
    `/assets/{assetKey}/sprite.jpg`, `/assets/{assetKey}/sprite.vtt` — Chandler
    serves the object key directly, no version resolution. assetKey is stream_id
    for live streams; clip_hash / dvr_hash / vod_hash (= artifact_hash) for artifacts."""

    poster_url: str = Field(alias="posterUrl")
    sprite_vtt_url: str = Field(alias="spriteVttUrl")
    sprite_jpg_url: str = Field(alias="spriteJpgUrl")
    asset_key: str = Field(alias="assetKey")


class ConnectionEventInNodeDefaultStreamRetentionOverrides(BaseModel):
    """Per-stream retention overrides for DVR and clips. VOD uploads aren't
    stream-bound, so they have no stream-level knob here."""

    stream_id: str = Field(alias="streamId")
    dvr_retention_days_override: Optional[int] = Field(
        alias="dvrRetentionDaysOverride",
        description="Null = no override (inherit tenant default). 0 = no auto-expire.",
    )
    "Null = no override (inherit tenant default). 0 = no auto-expire."
    clip_retention_days_override: Optional[int] = Field(
        alias="clipRetentionDaysOverride"
    )


class ConnectionEventInNodeDefaultClientBucket(BaseModel):
    h_3_index: str = Field(alias="h3Index")
    resolution: int


class ConnectionEventInNodeDefaultNodeBucket(BaseModel):
    h_3_index: str = Field(alias="h3Index")
    resolution: int


class ConversationDefault(BaseModel):
    """A support conversation between tenant and support team.
    Conversations can contain multiple messages and have a status."""

    id: str = Field(description="The globally unique identifier for this conversation.")
    "The globally unique identifier for this conversation."
    subject: Optional[str] = Field(
        description="Optional subject line for the conversation."
    )
    "Optional subject line for the conversation."
    status: ConversationStatus = Field(
        description="Current status of the conversation."
    )
    "Current status of the conversation."
    last_message: Optional["ConversationDefaultLastMessage"] = Field(
        alias="lastMessage", description="The last message in this conversation."
    )
    "The last message in this conversation."
    unread_count: int = Field(
        alias="unreadCount", description="Number of unread messages."
    )
    "Number of unread messages."
    created_at: datetime = Field(
        alias="createdAt", description="When the conversation was created."
    )
    "When the conversation was created."
    updated_at: datetime = Field(
        alias="updatedAt", description="When the conversation was last updated."
    )
    "When the conversation was last updated."


class ConversationDefaultLastMessage(BaseModel):
    """A message within a support conversation."""

    id: str = Field(description="The globally unique identifier for this message.")
    "The globally unique identifier for this message."
    conversation_id: str = Field(
        alias="conversationId", description="The conversation this message belongs to."
    )
    "The conversation this message belongs to."
    content: str = Field(description="The message content.")
    "The message content."
    sender: MessageSender = Field(description="Who sent this message.")
    "Who sent this message."
    created_at: datetime = Field(
        alias="createdAt", description="When the message was sent."
    )
    "When the message was sent."


class ConversationInNodeDefault(BaseModel):
    """A support conversation between tenant and support team.
    Conversations can contain multiple messages and have a status."""

    id: str = Field(description="The globally unique identifier for this conversation.")
    "The globally unique identifier for this conversation."
    subject: Optional[str] = Field(
        description="Optional subject line for the conversation."
    )
    "Optional subject line for the conversation."
    conversation_status: ConversationStatus = Field(
        alias="conversationStatus", description="Current status of the conversation."
    )
    "Current status of the conversation."
    last_message: Optional["ConversationInNodeDefaultLastMessage"] = Field(
        alias="lastMessage", description="The last message in this conversation."
    )
    "The last message in this conversation."
    unread_count: int = Field(
        alias="unreadCount", description="Number of unread messages."
    )
    "Number of unread messages."
    created_at: datetime = Field(
        alias="createdAt", description="When the conversation was created."
    )
    "When the conversation was created."
    conversation_updated_at: datetime = Field(
        alias="conversationUpdatedAt",
        description="When the conversation was last updated.",
    )
    "When the conversation was last updated."


class ConversationInNodeDefaultLastMessage(BaseModel):
    """A message within a support conversation."""

    id: str = Field(description="The globally unique identifier for this message.")
    "The globally unique identifier for this message."
    conversation_id: str = Field(
        alias="conversationId", description="The conversation this message belongs to."
    )
    "The conversation this message belongs to."
    content: str = Field(description="The message content.")
    "The message content."
    sender: MessageSender = Field(description="Who sent this message.")
    "Who sent this message."
    created_at: datetime = Field(
        alias="createdAt", description="When the message was sent."
    )
    "When the message was sent."


class CreateEdgeClusterResponseDefault(BaseModel):
    cluster: "CreateEdgeClusterResponseDefaultCluster"
    bootstrap_token: "CreateEdgeClusterResponseDefaultBootstrapToken" = Field(
        alias="bootstrapToken"
    )
    foghorn_addr: str = Field(alias="foghornAddr")


class CreateEdgeClusterResponseDefaultCluster(BaseModel):
    """A streaming cluster containing one or more infrastructure nodes.
    Clusters provide isolated capacity for streaming workloads."""

    id: str = Field(description="Global unique identifier for Relay compatibility.")
    "Global unique identifier for Relay compatibility."
    cluster_id: str = Field(
        alias="clusterId", description="Internal cluster identifier (UUID)."
    )
    "Internal cluster identifier (UUID)."
    cluster_name: str = Field(
        alias="clusterName", description="Human-readable cluster name."
    )
    "Human-readable cluster name."
    cluster_type: str = Field(
        alias="clusterType",
        description="Cluster architecture type (dedicated, shared, hybrid).",
    )
    "Cluster architecture type (dedicated, shared, hybrid)."
    deployment_model: str = Field(
        alias="deploymentModel",
        description="Deployment model (platform, private, hybrid).",
    )
    "Deployment model (platform, private, hybrid)."
    base_url: str = Field(
        alias="baseUrl", description="Base URL for cluster API access."
    )
    "Base URL for cluster API access."
    database_url: Optional[str] = Field(
        alias="databaseUrl", description="Internal database connection URL."
    )
    "Internal database connection URL."
    periscope_url: Optional[str] = Field(
        alias="periscopeUrl", description="Analytics query endpoint URL."
    )
    "Analytics query endpoint URL."
    kafka_brokers: Optional[list[str]] = Field(
        alias="kafkaBrokers", description="Kafka broker addresses for event streaming."
    )
    "Kafka broker addresses for event streaming."
    max_concurrent_streams: int = Field(
        alias="maxConcurrentStreams", description="Maximum allowed concurrent streams."
    )
    "Maximum allowed concurrent streams."
    max_concurrent_viewers: int = Field(
        alias="maxConcurrentViewers", description="Maximum allowed concurrent viewers."
    )
    "Maximum allowed concurrent viewers."
    max_bandwidth_mbps: int = Field(
        alias="maxBandwidthMbps", description="Maximum allowed bandwidth in Mbps."
    )
    "Maximum allowed bandwidth in Mbps."
    health_status: str = Field(
        alias="healthStatus",
        description="Cluster health status (healthy, degraded, critical).",
    )
    "Cluster health status (healthy, degraded, critical)."
    is_active: bool = Field(
        alias="isActive", description="Whether the cluster is operational."
    )
    "Whether the cluster is operational."
    is_default_cluster: bool = Field(
        alias="isDefaultCluster",
        description="Whether this is the tenant's default cluster.",
    )
    "Whether this is the tenant's default cluster."
    is_platform_official: bool = Field(
        alias="isPlatformOfficial",
        description="Whether this cluster is a platform-official regional/control cell.",
    )
    "Whether this cluster is a platform-official regional/control cell."
    region_id: Optional[str] = Field(
        alias="regionId", description="Operational region identifier for this cluster."
    )
    "Operational region identifier for this cluster."
    is_subscribed: bool = Field(
        alias="isSubscribed", description="Whether the current tenant is subscribed."
    )
    "Whether the current tenant is subscribed."
    created_at: Optional[datetime] = Field(
        alias="createdAt", description="When the cluster was created."
    )
    "When the cluster was created."
    updated_at: Optional[datetime] = Field(
        alias="updatedAt", description="When the cluster was last updated."
    )
    "When the cluster was last updated."
    owner_tenant_id: Optional[str] = Field(
        alias="ownerTenantId",
        description="Tenant that owns this cluster (for private clusters).",
    )
    "Tenant that owns this cluster (for private clusters)."
    visibility: ClusterVisibility = Field(
        description="Marketplace visibility (PUBLIC, PRIVATE, INVITE_ONLY)."
    )
    "Marketplace visibility (PUBLIC, PRIVATE, INVITE_ONLY)."
    pricing_model: ClusterPricingModel = Field(
        alias="pricingModel", description="Pricing model for subscriptions."
    )
    "Pricing model for subscriptions."
    monthly_price_cents: Optional[int] = Field(
        alias="monthlyPriceCents", description="Monthly subscription price in cents."
    )
    "Monthly subscription price in cents."
    requires_approval: bool = Field(
        alias="requiresApproval", description="Whether access requires owner approval."
    )
    "Whether access requires owner approval."
    short_description: Optional[str] = Field(
        alias="shortDescription", description="Short marketplace description."
    )
    "Short marketplace description."


class CreateEdgeClusterResponseDefaultBootstrapToken(BaseModel):
    """A bootstrap token for provisioning new infrastructure nodes.
    Used during initial node registration to authenticate join requests."""

    id: str = Field(description="Unique token identifier.")
    "Unique token identifier."
    name: str = Field(description="Human-readable name for the token.")
    "Human-readable name for the token."
    token: Optional[str] = Field(
        description="The secret token value (only returned on creation)."
    )
    "The secret token value (only returned on creation)."
    kind: str = Field(description="Token type (edge_node, service).")
    "Token type (edge_node, service)."
    cluster_id: Optional[str] = Field(
        alias="clusterId",
        description="Target cluster UUID for node tokens (not the Relay ID).",
    )
    "Target cluster UUID for node tokens (not the Relay ID)."
    expected_ip: Optional[str] = Field(
        alias="expectedIp", description="Expected IP address for validation."
    )
    "Expected IP address for validation."
    metadata: Optional[Any] = Field(description="Additional metadata for the token.")
    "Additional metadata for the token."
    usage_limit: Optional[int] = Field(
        alias="usageLimit",
        description="Maximum number of times this token can be used.",
    )
    "Maximum number of times this token can be used."
    usage_count: int = Field(
        alias="usageCount", description="Number of times this token has been used."
    )
    "Number of times this token has been used."
    expires_at: Optional[datetime] = Field(
        alias="expiresAt", description="When the token expires."
    )
    "When the token expires."
    used_at: Optional[datetime] = Field(
        alias="usedAt", description="When the token was last used."
    )
    "When the token was last used."
    created_by: Optional[str] = Field(
        alias="createdBy", description="User who created the token."
    )
    "User who created the token."
    created_at: datetime = Field(
        alias="createdAt", description="When the token was created."
    )
    "When the token was created."


class CreateEnrollmentTokenResponseDefault(BaseModel):
    bootstrap_token: "CreateEnrollmentTokenResponseDefaultBootstrapToken" = Field(
        alias="bootstrapToken"
    )


class CreateEnrollmentTokenResponseDefaultBootstrapToken(BaseModel):
    """A bootstrap token for provisioning new infrastructure nodes.
    Used during initial node registration to authenticate join requests."""

    id: str = Field(description="Unique token identifier.")
    "Unique token identifier."
    name: str = Field(description="Human-readable name for the token.")
    "Human-readable name for the token."
    token: Optional[str] = Field(
        description="The secret token value (only returned on creation)."
    )
    "The secret token value (only returned on creation)."
    kind: str = Field(description="Token type (edge_node, service).")
    "Token type (edge_node, service)."
    cluster_id: Optional[str] = Field(
        alias="clusterId",
        description="Target cluster UUID for node tokens (not the Relay ID).",
    )
    "Target cluster UUID for node tokens (not the Relay ID)."
    expected_ip: Optional[str] = Field(
        alias="expectedIp", description="Expected IP address for validation."
    )
    "Expected IP address for validation."
    metadata: Optional[Any] = Field(description="Additional metadata for the token.")
    "Additional metadata for the token."
    usage_limit: Optional[int] = Field(
        alias="usageLimit",
        description="Maximum number of times this token can be used.",
    )
    "Maximum number of times this token can be used."
    usage_count: int = Field(
        alias="usageCount", description="Number of times this token has been used."
    )
    "Number of times this token has been used."
    expires_at: Optional[datetime] = Field(
        alias="expiresAt", description="When the token expires."
    )
    "When the token expires."
    used_at: Optional[datetime] = Field(
        alias="usedAt", description="When the token was last used."
    )
    "When the token was last used."
    created_by: Optional[str] = Field(
        alias="createdBy", description="User who created the token."
    )
    "User who created the token."
    created_at: datetime = Field(
        alias="createdAt", description="When the token was created."
    )
    "When the token was created."


class CryptoTopupResultDefault(BaseModel):
    """Result from creating a crypto top-up.

    The price is locked at this response. Send exactly `expectedAmountToken` of
    `asset` to `depositAddress`; on confirmation, the balance is credited at
    `quotedPriceUsd` regardless of price drift inside the address TTL."""

    topup_id: str = Field(
        alias="topupId", description="Internal top-up ID for tracking/polling."
    )
    "Internal top-up ID for tracking/polling."
    deposit_address: str = Field(
        alias="depositAddress",
        description="HD-derived Ethereum address to send funds to.",
    )
    "HD-derived Ethereum address to send funds to."
    asset: CryptoAsset = Field(description="Asset to send (ETH or USDC).")
    "Asset to send (ETH or USDC)."
    asset_symbol: str = Field(
        alias="assetSymbol", description='Human-readable asset symbol ("ETH" | "USDC").'
    )
    'Human-readable asset symbol ("ETH" | "USDC").'
    expected_amount_cents: int = Field(
        alias="expectedAmountCents",
        description="Echo of input amountCents, in cents of the tenant's presentment currency.",
    )
    "Echo of input amountCents, in cents of the tenant's presentment currency."
    expires_at: datetime = Field(
        alias="expiresAt",
        description="When this deposit address expires (24 hours from creation).",
    )
    "When this deposit address expires (24 hours from creation)."
    expected_amount_base_units: str = Field(
        alias="expectedAmountBaseUnits",
        description="Token base units to send (decimal string; wei for 18-decimal assets).",
    )
    "Token base units to send (decimal string; wei for 18-decimal assets)."
    expected_amount_token: str = Field(
        alias="expectedAmountToken",
        description='Human-friendly decimal of expectedAmountBaseUnits, e.g. "0.01515".',
    )
    'Human-friendly decimal of expectedAmountBaseUnits, e.g. "0.01515".'
    quoted_price_usd: str = Field(
        alias="quotedPriceUsd",
        description='Locked USD price per 1 whole token, e.g. "3300.45".',
    )
    'Locked USD price per 1 whole token, e.g. "3300.45".'
    quote_source: str = Field(
        alias="quoteSource",
        description='Source of the price quote ("chainlink" | "one_to_one").',
    )
    'Source of the price quote ("chainlink" | "one_to_one").'
    quoted_at: datetime = Field(
        alias="quotedAt", description="When the price was locked."
    )
    "When the price was locked."
    network: str = Field(
        description='Network the deposit address lives on ("ethereum" | "arbitrum" | "base").'
    )
    'Network the deposit address lives on ("ethereum" | "arbitrum" | "base").'
    conversion: Optional["CryptoTopupResultDefaultConversion"] = Field(
        description="Presentment amount and the EUR credit it converts to, locked with the quote."
    )
    "Presentment amount and the EUR credit it converts to, locked with the quote."


class CryptoTopupResultDefaultConversion(BaseModel):
    """Conversion of a payment into the EUR prepaid ledger and invoice currency.
    EUR amounts use the identity rate."""

    original_amount_cents: int = Field(
        alias="originalAmountCents",
        description="Amount charged, in cents of `originalCurrency`.",
    )
    "Amount charged, in cents of `originalCurrency`."
    original_currency: Any = Field(
        alias="originalCurrency", description="Currency charged (EUR, USD, GBP)."
    )
    "Currency charged (EUR, USD, GBP)."
    eur_amount_cents: int = Field(
        alias="eurAmountCents", description="EUR cents credited or applied."
    )
    "EUR cents credited or applied."
    units_per_eur: str = Field(
        alias="unitsPerEur",
        description='Units of `originalCurrency` one euro buys, as a decimal string ("1" for EUR).',
    )
    'Units of `originalCurrency` one euro buys, as a decimal string ("1" for EUR).'
    source: str = Field(
        description="Rate source: identity (EUR), ecb (ECB reference rate), or legacy_quote (USD rate quoted before ECB rates were stored)."
    )
    "Rate source: identity (EUR), ecb (ECB reference rate), or legacy_quote (USD rate quoted before ECB rates were stored)."
    reference_date: str = Field(
        alias="referenceDate",
        description="ECB reference date of the rate (YYYY-MM-DD).",
    )
    "ECB reference date of the rate (YYYY-MM-DD)."


class CryptoTopupStatusDefault(BaseModel):
    """Status of a crypto top-up (for polling).

    `status` cycles: pending → confirming → completed (or expired)."""

    id: str = Field(description="Top-up ID.")
    "Top-up ID."
    deposit_address: str = Field(alias="depositAddress", description="Deposit address.")
    "Deposit address."
    asset: CryptoAsset = Field(description="Asset being received.")
    "Asset being received."
    status: str = Field(
        description="Current status: pending, confirming, completed, expired."
    )
    "Current status: pending, confirming, completed, expired."
    tx_hash: Optional[str] = Field(
        alias="txHash", description="Blockchain transaction hash (when detected)."
    )
    "Blockchain transaction hash (when detected)."
    confirmations: int = Field(description="Number of block confirmations.")
    "Number of block confirmations."
    received_amount_base_units: Optional[str] = Field(
        alias="receivedAmountBaseUnits",
        description="Amount received in token base units (decimal string).",
    )
    "Amount received in token base units (decimal string)."
    received_amount_token: Optional[str] = Field(
        alias="receivedAmountToken",
        description="Human-friendly decimal of receivedAmountBaseUnits.",
    )
    "Human-friendly decimal of receivedAmountBaseUnits."
    credited_amount_cents: Optional[int] = Field(
        alias="creditedAmountCents",
        description="Amount credited to balance in `creditedAmountCurrency` cents.",
    )
    "Amount credited to balance in `creditedAmountCurrency` cents."
    credited_amount_currency: Optional[str] = Field(
        alias="creditedAmountCurrency",
        description="Ledger currency of the credit, EUR.",
    )
    "Ledger currency of the credit, EUR."
    quote_source: Optional[str] = Field(
        alias="quoteSource",
        description='Source of the locked price quote ("chainlink" | "one_to_one").',
    )
    'Source of the locked price quote ("chainlink" | "one_to_one").'
    network: Optional[str] = Field(description="Network the deposit address lives on.")
    "Network the deposit address lives on."
    expires_at: datetime = Field(
        alias="expiresAt", description="When the deposit address expires."
    )
    "When the deposit address expires."
    detected_at: Optional[datetime] = Field(
        alias="detectedAt", description="When payment was first detected on-chain."
    )
    "When payment was first detected on-chain."
    completed_at: Optional[datetime] = Field(
        alias="completedAt", description="When balance was credited."
    )
    "When balance was credited."
    conversion: Optional["CryptoTopupStatusDefaultConversion"] = Field(
        description="Presentment amount and the EUR credit it converts to, locked with the quote."
    )
    "Presentment amount and the EUR credit it converts to, locked with the quote."


class CryptoTopupStatusDefaultConversion(BaseModel):
    """Conversion of a payment into the EUR prepaid ledger and invoice currency.
    EUR amounts use the identity rate."""

    original_amount_cents: int = Field(
        alias="originalAmountCents",
        description="Amount charged, in cents of `originalCurrency`.",
    )
    "Amount charged, in cents of `originalCurrency`."
    original_currency: Any = Field(
        alias="originalCurrency", description="Currency charged (EUR, USD, GBP)."
    )
    "Currency charged (EUR, USD, GBP)."
    eur_amount_cents: int = Field(
        alias="eurAmountCents", description="EUR cents credited or applied."
    )
    "EUR cents credited or applied."
    units_per_eur: str = Field(
        alias="unitsPerEur",
        description='Units of `originalCurrency` one euro buys, as a decimal string ("1" for EUR).',
    )
    'Units of `originalCurrency` one euro buys, as a decimal string ("1" for EUR).'
    source: str = Field(
        description="Rate source: identity (EUR), ecb (ECB reference rate), or legacy_quote (USD rate quoted before ECB rates were stored)."
    )
    "Rate source: identity (EUR), ecb (ECB reference rate), or legacy_quote (USD rate quoted before ECB rates were stored)."
    reference_date: str = Field(
        alias="referenceDate",
        description="ECB reference date of the rate (YYYY-MM-DD).",
    )
    "ECB reference date of the rate (YYYY-MM-DD)."


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


class DeleteSuccessDefault(BaseModel):
    success: bool
    deleted_id: str = Field(alias="deletedId")
    pending: Optional[bool] = Field(
        description="True when the delete was accepted but is NOT yet finalized (e.g. a stream deletion awaiting the serving cell's\ncleanup-tombstone acknowledgement). The operation converges asynchronously; false means fully deleted."
    )
    "True when the delete was accepted but is NOT yet finalized (e.g. a stream deletion awaiting the serving cell's\ncleanup-tombstone acknowledgement). The operation converges asynchronously; false means fully deleted."


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
        description="List of granted permission scopes (streams:read, streams:write, etc.)."
    )
    "List of granted permission scopes (streams:read, streams:write, etc.)."
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


class EffectiveRetentionDefault(BaseModel):
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


class FederationEventDefault(BaseModel):
    timestamp: datetime
    event_type: str = Field(alias="eventType")
    local_cluster: str = Field(alias="localCluster")
    remote_cluster: str = Field(alias="remoteCluster")
    stream_name: Optional[str] = Field(alias="streamName")
    stream_id: Optional[str] = Field(alias="streamId")
    source_node: Optional[str] = Field(alias="sourceNode")
    dest_node: Optional[str] = Field(alias="destNode")
    dtsc_url: Optional[str] = Field(alias="dtscUrl")
    latency_ms: Optional[float] = Field(alias="latencyMs")
    time_to_live_ms: Optional[float] = Field(alias="timeToLiveMs")
    failure_reason: Optional[str] = Field(alias="failureReason")
    queried_clusters: Optional[int] = Field(alias="queriedClusters")
    responding_clusters: Optional[int] = Field(alias="respondingClusters")
    total_candidates: Optional[int] = Field(alias="totalCandidates")
    best_remote_score: Optional[int] = Field(alias="bestRemoteScore")
    peer_cluster: Optional[str] = Field(alias="peerCluster")
    role: str
    reason: Optional[str]
    stream_tenant_id: Optional[str] = Field(alias="streamTenantId")
    control_cell_id: Optional[str] = Field(alias="controlCellId")
    origin_cluster_id: Optional[str] = Field(alias="originClusterId")


class FederationSummaryDefault(BaseModel):
    event_counts: list["FederationSummaryDefaultEventCounts"] = Field(
        alias="eventCounts"
    )
    total_events: int = Field(alias="totalEvents")
    overall_avg_latency_ms: float = Field(alias="overallAvgLatencyMs")
    overall_failure_rate: float = Field(alias="overallFailureRate")


class FederationSummaryDefaultEventCounts(BaseModel):
    event_type: str = Field(alias="eventType")
    count: int
    failure_count: int = Field(alias="failureCount")
    avg_latency_ms: float = Field(alias="avgLatencyMs")


class GeographicDistributionDefault(BaseModel):
    time_range: "GeographicDistributionDefaultTimeRange" = Field(alias="timeRange")
    stream_id: Optional[str] = Field(alias="streamId")
    stream: Optional["GeographicDistributionDefaultStream"]
    top_countries: list["GeographicDistributionDefaultTopCountries"] = Field(
        alias="topCountries"
    )
    top_cities: list["GeographicDistributionDefaultTopCities"] = Field(
        alias="topCities"
    )
    unique_countries: int = Field(alias="uniqueCountries")
    unique_cities: int = Field(alias="uniqueCities")
    total_viewers: int = Field(alias="totalViewers")
    viewers_by_country: list["GeographicDistributionDefaultViewersByCountry"] = Field(
        alias="viewersByCountry"
    )


class GeographicDistributionDefaultTimeRange(BaseModel):
    """Time range returned in query results."""

    start: datetime = Field(description="Start of the time range.")
    "Start of the time range."
    end: datetime = Field(description="End of the time range.")
    "End of the time range."


class GeographicDistributionDefaultStream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

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
        description="Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path.",
    )
    "Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path."
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
    pull_source: Optional["GeographicDistributionDefaultStreamPullSource"] = Field(
        alias="pullSource",
        description="Pull-source config for pull streams; null for push streams.",
    )
    "Pull-source config for pull streams; null for push streams."
    managed_source: Optional["GeographicDistributionDefaultStreamManagedSource"] = (
        Field(
            alias="managedSource",
            description="Safe source summary for managed streams; null for push and pull streams.",
        )
    )
    "Safe source summary for managed streams; null for push and pull streams."
    source_location: "GeographicDistributionDefaultStreamSourceLocation" = Field(
        alias="sourceLocation",
        description="Where the stream's source may be ingested, derived from the stream's own ingest placement rules.",
    )
    "Where the stream's source may be ingested, derived from the stream's own ingest placement rules."
    created_at: datetime = Field(
        alias="createdAt", description="When this stream was created."
    )
    "When this stream was created."
    updated_at: datetime = Field(
        alias="updatedAt", description="When this stream was last modified."
    )
    "When this stream was last modified."
    metrics: Optional["GeographicDistributionDefaultStreamMetrics"] = Field(
        description="Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    )
    "Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    push_targets: list["GeographicDistributionDefaultStreamPushTargets"] = Field(
        alias="pushTargets",
        description="Configured multistream push targets for this stream.",
    )
    "Configured multistream push targets for this stream."
    playback_policy: Optional["GeographicDistributionDefaultStreamPlaybackPolicy"] = (
        Field(
            alias="playbackPolicy",
            description="Playback access policy. null/PUBLIC = anyone with the playbackId can watch.",
        )
    )
    "Playback access policy. null/PUBLIC = anyone with the playbackId can watch."
    dvr_chapter_mode: Optional[DVRChapterMode] = Field(
        alias="dvrChapterMode",
        description="How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast.",
    )
    "How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast."
    dvr_chapter_interval_seconds: Optional[int] = Field(
        alias="dvrChapterIntervalSeconds",
        description="Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour).",
    )
    "Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour)."
    monitoring: MonitoringToggle = Field(
        description="Per-stream Skipper monitoring override (INHERIT follows tier)."
    )
    "Per-stream Skipper monitoring override (INHERIT follows tier)."
    thumbnail_assets: Optional["GeographicDistributionDefaultStreamThumbnailAssets"] = (
        Field(
            alias="thumbnailAssets",
            description="Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles.",
        )
    )
    "Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles."
    retention_overrides: Optional[
        "GeographicDistributionDefaultStreamRetentionOverrides"
    ] = Field(
        alias="retentionOverrides",
        description="Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here.",
    )
    "Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here."


class GeographicDistributionDefaultStreamPullSource(BaseModel):
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


class GeographicDistributionDefaultStreamManagedSource(BaseModel):
    """Safe summary of an operator-managed source. Literal paths, commands, and
    credentials are intentionally not exposed through the tenant API."""

    source_kind: str = Field(
        alias="sourceKind", description="Managed source form: file, playlist, or exec."
    )
    "Managed source form: file, playlist, or exec."
    always_on: bool = Field(
        alias="alwaysOn",
        description="Whether the source is kept active without waiting for a viewer.",
    )
    "Whether the source is kept active without waiting for a viewer."
    placement_count: int = Field(
        alias="placementCount",
        description="Number of source placements requested by the operator configuration.",
    )
    "Number of source placements requested by the operator configuration."


class GeographicDistributionDefaultStreamSourceLocation(BaseModel):
    mode: SourceLocationMode
    avoid_node_ids: list[str] = Field(
        alias="avoidNodeIds", description="Empty unless mode is RESTRICTED."
    )
    "Empty unless mode is RESTRICTED."


class GeographicDistributionDefaultStreamMetrics(BaseModel):
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
    node_id: Optional[str] = Field(
        alias="nodeId", description="ID of the edge node handling this stream."
    )
    "ID of the edge node handling this stream."
    track_count: Optional[int] = Field(
        alias="trackCount", description="Number of quality tracks available."
    )
    "Number of quality tracks available."
    total_inputs: Optional[int] = Field(
        alias="totalInputs", description="Total ingest connections to this stream."
    )
    "Total ingest connections to this stream."
    uploaded_bytes: float = Field(
        alias="uploadedBytes",
        description="Total bytes uploaded (ingested) for current session.",
    )
    "Total bytes uploaded (ingested) for current session."
    downloaded_bytes: float = Field(
        alias="downloadedBytes",
        description="Total bytes downloaded (egress) for current session.",
    )
    "Total bytes downloaded (egress) for current session."
    viewer_seconds: float = Field(
        alias="viewerSeconds", description="Total viewer-seconds accumulated."
    )
    "Total viewer-seconds accumulated."
    packets_sent: Optional[float] = Field(
        alias="packetsSent", description="Total packets sent to viewers."
    )
    "Total packets sent to viewers."
    packets_lost: Optional[float] = Field(
        alias="packetsLost", description="Total packets lost in transit."
    )
    "Total packets lost in transit."
    packets_retransmitted: Optional[float] = Field(
        alias="packetsRetransmitted", description="Total packets retransmitted."
    )
    "Total packets retransmitted."
    buffer_state: Optional[str] = Field(
        alias="bufferState",
        description="Buffer health state (HEALTHY, WARNING, CRITICAL).",
    )
    "Buffer health state (HEALTHY, WARNING, CRITICAL)."
    quality_tier: Optional[str] = Field(
        alias="qualityTier",
        description="Highest quality tier available (4K, 1080p, 720p, etc.).",
    )
    "Highest quality tier available (4K, 1080p, 720p, etc.)."
    primary_width: Optional[int] = Field(
        alias="primaryWidth", description="Primary video track width in pixels."
    )
    "Primary video track width in pixels."
    primary_height: Optional[int] = Field(
        alias="primaryHeight", description="Primary video track height in pixels."
    )
    "Primary video track height in pixels."
    primary_fps: Optional[float] = Field(
        alias="primaryFps", description="Primary video track framerate."
    )
    "Primary video track framerate."
    primary_codec: Optional[str] = Field(
        alias="primaryCodec",
        description="Primary video codec (H.264, H.265, VP9, AV1).",
    )
    "Primary video codec (H.264, H.265, VP9, AV1)."
    primary_bitrate: Optional[int] = Field(
        alias="primaryBitrate", description="Primary video bitrate in kbps."
    )
    "Primary video bitrate in kbps."
    has_issues: Optional[bool] = Field(
        alias="hasIssues", description="Whether the stream has active quality issues."
    )
    "Whether the stream has active quality issues."
    issues_description: Optional[str] = Field(
        alias="issuesDescription",
        description="Human-readable description of current issues.",
    )
    "Human-readable description of current issues."


class GeographicDistributionDefaultStreamPushTargets(BaseModel):
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


class GeographicDistributionDefaultStreamPlaybackPolicy(BaseModel):
    """Per-playback-object access policy. Foghorn reads this in the USER_NEW
    trigger handler; the requiresAuth marker (on the playback object itself)
    gates whether the full policy is fetched at all."""

    type_: PlaybackPolicyType = Field(alias="type")
    allowed_origins: list[str] = Field(
        alias="allowedOrigins",
        description="Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied.",
    )
    "Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied."


class GeographicDistributionDefaultStreamThumbnailAssets(BaseModel):
    """Chandler-served thumbnail asset URLs (poster, sprite, VTT cues). URL shape
    is `{chandlerBase}/assets/{assetKey}/poster.jpg`,
    `/assets/{assetKey}/sprite.jpg`, `/assets/{assetKey}/sprite.vtt` — Chandler
    serves the object key directly, no version resolution. assetKey is stream_id
    for live streams; clip_hash / dvr_hash / vod_hash (= artifact_hash) for artifacts."""

    poster_url: str = Field(alias="posterUrl")
    sprite_vtt_url: str = Field(alias="spriteVttUrl")
    sprite_jpg_url: str = Field(alias="spriteJpgUrl")
    asset_key: str = Field(alias="assetKey")


class GeographicDistributionDefaultStreamRetentionOverrides(BaseModel):
    """Per-stream retention overrides for DVR and clips. VOD uploads aren't
    stream-bound, so they have no stream-level knob here."""

    stream_id: str = Field(alias="streamId")
    dvr_retention_days_override: Optional[int] = Field(
        alias="dvrRetentionDaysOverride",
        description="Null = no override (inherit tenant default). 0 = no auto-expire.",
    )
    "Null = no override (inherit tenant default). 0 = no auto-expire."
    clip_retention_days_override: Optional[int] = Field(
        alias="clipRetentionDaysOverride"
    )


class GeographicDistributionDefaultTopCountries(BaseModel):
    country_code: str = Field(alias="countryCode")
    viewer_count: int = Field(alias="viewerCount")
    percentage: float


class GeographicDistributionDefaultTopCities(BaseModel):
    city: str
    country_code: Optional[str] = Field(alias="countryCode")
    viewer_count: int = Field(alias="viewerCount")
    percentage: float
    latitude: Optional[float]
    longitude: Optional[float]


class GeographicDistributionDefaultViewersByCountry(BaseModel):
    timestamp: datetime
    country_code: str = Field(alias="countryCode")
    viewer_count: int = Field(alias="viewerCount")


class IncidentDefault(BaseModel):
    id: str
    scope: IncidentScope
    tenant_id: Optional[str] = Field(
        alias="tenantId", description="Null for platform-scope incidents."
    )
    "Null for platform-scope incidents."
    cluster_id: Optional[str] = Field(alias="clusterId")
    region: Optional[str]
    alertname: str
    severity: str
    status: IncidentStatus
    resolution: Optional[IncidentResolution] = Field(
        description="Null until the incident is resolved."
    )
    "Null until the incident is resolved."
    title: str
    summary: Optional[str]
    firing_alert_count: int = Field(
        alias="firingAlertCount",
        description="Alerts of this incident that are still firing.",
    )
    "Alerts of this incident that are still firing."
    started_at: datetime = Field(alias="startedAt")
    last_alert_at: datetime = Field(alias="lastAlertAt")
    acknowledged_at: Optional[datetime] = Field(alias="acknowledgedAt")
    acknowledged_by: Optional[str] = Field(alias="acknowledgedBy")
    assigned_to: Optional[str] = Field(alias="assignedTo")
    resolved_at: Optional[datetime] = Field(alias="resolvedAt")
    resolved_by: Optional[str] = Field(alias="resolvedBy")
    created_at: datetime = Field(alias="createdAt")
    updated_at: datetime = Field(alias="updatedAt")


class IncidentDetailDefault(BaseModel):
    incident: "IncidentDetailDefaultIncident"
    alerts: list["IncidentDetailDefaultAlerts"]
    timeline: list["IncidentDetailDefaultTimeline"] = Field(description="Oldest first.")
    "Oldest first."


class IncidentDetailDefaultIncident(BaseModel):
    id: str
    scope: IncidentScope
    tenant_id: Optional[str] = Field(
        alias="tenantId", description="Null for platform-scope incidents."
    )
    "Null for platform-scope incidents."
    cluster_id: Optional[str] = Field(alias="clusterId")
    region: Optional[str]
    alertname: str
    severity: str
    status: IncidentStatus
    resolution: Optional[IncidentResolution] = Field(
        description="Null until the incident is resolved."
    )
    "Null until the incident is resolved."
    title: str
    summary: Optional[str]
    firing_alert_count: int = Field(
        alias="firingAlertCount",
        description="Alerts of this incident that are still firing.",
    )
    "Alerts of this incident that are still firing."
    started_at: datetime = Field(alias="startedAt")
    last_alert_at: datetime = Field(alias="lastAlertAt")
    acknowledged_at: Optional[datetime] = Field(alias="acknowledgedAt")
    acknowledged_by: Optional[str] = Field(alias="acknowledgedBy")
    assigned_to: Optional[str] = Field(alias="assignedTo")
    resolved_at: Optional[datetime] = Field(alias="resolvedAt")
    resolved_by: Optional[str] = Field(alias="resolvedBy")
    created_at: datetime = Field(alias="createdAt")
    updated_at: datetime = Field(alias="updatedAt")


class IncidentDetailDefaultAlerts(BaseModel):
    fingerprint: str
    status: str = Field(description="firing or resolved.")
    "firing or resolved."
    labels: Any
    annotations: Any
    starts_at: datetime = Field(alias="startsAt")
    ends_at: Optional[datetime] = Field(alias="endsAt")
    generator_url: Optional[str] = Field(alias="generatorUrl")


class IncidentDetailDefaultTimeline(BaseModel):
    id: str
    kind: IncidentEventKind
    actor_user_id: Optional[str] = Field(
        alias="actorUserId",
        description="Null for events produced by alerting or services.",
    )
    "Null for events produced by alerting or services."
    created_at: datetime = Field(alias="createdAt")
    note: Optional[str] = Field(description="NOTE events.")
    "NOTE events."
    assigned_to: Optional[str] = Field(
        alias="assignedTo", description="ASSIGNED events; null clears the assignment."
    )
    "ASSIGNED events; null clears the assignment."
    report_id: Optional[str] = Field(
        alias="reportId",
        description="INVESTIGATION_ATTACHED events: the Skipper report.",
    )
    "INVESTIGATION_ATTACHED events: the Skipper report."
    channel: Optional[str] = Field(
        description="NOTIFIED events: email, slack, or discord."
    )
    "NOTIFIED events: email, slack, or discord."
    resolution: Optional[IncidentResolution] = Field(description="RESOLVED events.")
    "RESOLVED events."
    alert_fingerprint: Optional[str] = Field(
        alias="alertFingerprint", description="ALERT_FIRING and ALERT_RESOLVED events."
    )
    "ALERT_FIRING and ALERT_RESOLVED events."
    alertname: Optional[str] = Field(
        description="ALERT_FIRING and ALERT_RESOLVED events."
    )
    "ALERT_FIRING and ALERT_RESOLVED events."


class IncidentUpdatedEventDefault(BaseModel):
    """Change to an incident the subscriber can see."""

    incident_id: str = Field(alias="incidentId")
    cluster_id: Optional[str] = Field(alias="clusterId")
    status: IncidentStatus
    severity: str
    title: str
    change: str = Field(
        description="Timeline change that produced the update, e.g. opened, alert_firing, acknowledged, resolved."
    )
    "Timeline change that produced the update, e.g. opened, alert_firing, acknowledged, resolved."
    updated_at: datetime = Field(alias="updatedAt")


class InfrastructureNodeDefault(BaseModel):
    """An infrastructure node in a cluster (edge server, origin, transcoder).
    Nodes handle stream ingest, transcoding, and delivery to viewers."""

    id: str = Field(description="Global unique identifier for Relay compatibility.")
    "Global unique identifier for Relay compatibility."
    node_id: str = Field(alias="nodeId", description="Internal node identifier (UUID).")
    "Internal node identifier (UUID)."
    cluster_id: str = Field(
        alias="clusterId", description="Cluster this node belongs to."
    )
    "Cluster this node belongs to."
    node_name: str = Field(alias="nodeName", description="Human-readable node name.")
    "Human-readable node name."
    node_type: str = Field(
        alias="nodeType", description="Node role (edge, origin, transcoder, hybrid)."
    )
    "Node role (edge, origin, transcoder, hybrid)."
    internal_ip: Optional[str] = Field(
        alias="internalIp", description="Private network IP address."
    )
    "Private network IP address."
    external_ip: Optional[str] = Field(
        alias="externalIp", description="Public IP address for external access."
    )
    "Public IP address for external access."
    wireguard_ip: Optional[str] = Field(
        alias="wireguardIp", description="WireGuard mesh network IP."
    )
    "WireGuard mesh network IP."
    wireguard_public_key: Optional[str] = Field(
        alias="wireguardPublicKey", description="WireGuard public key for mesh peering."
    )
    "WireGuard public key for mesh peering."
    region: Optional[str] = Field(
        description="Geographic region (us-east, eu-west, etc.)."
    )
    "Geographic region (us-east, eu-west, etc.)."
    latitude: Optional[float] = Field(description="GPS latitude.")
    "GPS latitude."
    longitude: Optional[float] = Field(description="GPS longitude.")
    "GPS longitude."
    availability_zone: Optional[str] = Field(
        alias="availabilityZone", description="Availability zone within region."
    )
    "Availability zone within region."
    cpu_cores: Optional[int] = Field(
        alias="cpuCores", description="Number of CPU cores."
    )
    "Number of CPU cores."
    memory_gb: Optional[int] = Field(
        alias="memoryGb", description="Memory in gigabytes."
    )
    "Memory in gigabytes."
    disk_gb: Optional[int] = Field(
        alias="diskGb", description="Disk storage in gigabytes."
    )
    "Disk storage in gigabytes."
    last_heartbeat: Optional[datetime] = Field(
        alias="lastHeartbeat", description="Last heartbeat timestamp from node agent."
    )
    "Last heartbeat timestamp from node agent."
    tags: Optional[Any] = Field(description="Custom tags for node organization.")
    "Custom tags for node organization."
    metadata: Optional[Any] = Field(description="Additional node metadata.")
    "Additional node metadata."
    created_at: Optional[datetime] = Field(
        alias="createdAt", description="When the node was registered."
    )
    "When the node was registered."
    updated_at: Optional[datetime] = Field(
        alias="updatedAt", description="When the node was last updated."
    )
    "When the node was last updated."
    live_state: Optional["InfrastructureNodeDefaultLiveState"] = Field(
        alias="liveState", description="Real-time state from the analytics data plane."
    )
    "Real-time state from the analytics data plane."
    effective_mode: NodeOperationalMode = Field(
        alias="effectiveMode",
        description="Current operational mode (normal / draining / maintenance). Read from\nFoghorn's per-node heartbeat. Changes via setNodeMode.",
    )
    "Current operational mode (normal / draining / maintenance). Read from\nFoghorn's per-node heartbeat. Changes via setNodeMode."
    routing_impact_preview: "InfrastructureNodeDefaultRoutingImpactPreview" = Field(
        alias="routingImpactPreview",
        description="Current traffic on this node. Operators use these counts to understand\nhow much live traffic is affected before draining or entering maintenance.\nCounts come from Foghorn's GetNodeHealth.",
    )
    "Current traffic on this node. Operators use these counts to understand\nhow much live traffic is affected before draining or entering maintenance.\nCounts come from Foghorn's GetNodeHealth."


class InfrastructureNodeDefaultLiveState(BaseModel):
    node_id: str = Field(alias="nodeId")
    tenant_id: str = Field(alias="tenantId")
    cpu_percent: float = Field(alias="cpuPercent")
    ram_used_bytes: float = Field(
        alias="ramUsedBytes",
        description="Used memory in MiB (field name is historical).",
    )
    "Used memory in MiB (field name is historical)."
    ram_total_bytes: float = Field(
        alias="ramTotalBytes",
        description="Total memory in MiB (field name is historical).",
    )
    "Total memory in MiB (field name is historical)."
    disk_used_bytes: float = Field(alias="diskUsedBytes")
    disk_total_bytes: float = Field(alias="diskTotalBytes")
    up_speed: float = Field(alias="upSpeed")
    down_speed: float = Field(alias="downSpeed")
    active_streams: int = Field(alias="activeStreams")
    is_healthy: bool = Field(alias="isHealthy")
    latitude: float
    longitude: float
    location: str
    metadata: Optional[Any]
    updated_at: datetime = Field(alias="updatedAt")


class InfrastructureNodeDefaultRoutingImpactPreview(BaseModel):
    """Snapshot of how much traffic a node is currently serving."""

    active_streams: int = Field(alias="activeStreams")
    active_viewers: int = Field(alias="activeViewers")


class InfrastructureNodeInNodeDefault(BaseModel):
    """An infrastructure node in a cluster (edge server, origin, transcoder).
    Nodes handle stream ingest, transcoding, and delivery to viewers."""

    id: str = Field(description="Global unique identifier for Relay compatibility.")
    "Global unique identifier for Relay compatibility."
    node_id: str = Field(alias="nodeId", description="Internal node identifier (UUID).")
    "Internal node identifier (UUID)."
    infrastructure_node_cluster_id: str = Field(
        alias="infrastructureNodeClusterId", description="Cluster this node belongs to."
    )
    "Cluster this node belongs to."
    node_name: str = Field(alias="nodeName", description="Human-readable node name.")
    "Human-readable node name."
    node_type: str = Field(
        alias="nodeType", description="Node role (edge, origin, transcoder, hybrid)."
    )
    "Node role (edge, origin, transcoder, hybrid)."
    internal_ip: Optional[str] = Field(
        alias="internalIp", description="Private network IP address."
    )
    "Private network IP address."
    external_ip: Optional[str] = Field(
        alias="externalIp", description="Public IP address for external access."
    )
    "Public IP address for external access."
    wireguard_ip: Optional[str] = Field(
        alias="wireguardIp", description="WireGuard mesh network IP."
    )
    "WireGuard mesh network IP."
    wireguard_public_key: Optional[str] = Field(
        alias="wireguardPublicKey", description="WireGuard public key for mesh peering."
    )
    "WireGuard public key for mesh peering."
    region: Optional[str] = Field(
        description="Geographic region (us-east, eu-west, etc.)."
    )
    "Geographic region (us-east, eu-west, etc.)."
    latitude: Optional[float] = Field(description="GPS latitude.")
    "GPS latitude."
    longitude: Optional[float] = Field(description="GPS longitude.")
    "GPS longitude."
    availability_zone: Optional[str] = Field(
        alias="availabilityZone", description="Availability zone within region."
    )
    "Availability zone within region."
    cpu_cores: Optional[int] = Field(
        alias="cpuCores", description="Number of CPU cores."
    )
    "Number of CPU cores."
    memory_gb: Optional[int] = Field(
        alias="memoryGb", description="Memory in gigabytes."
    )
    "Memory in gigabytes."
    disk_gb: Optional[int] = Field(
        alias="diskGb", description="Disk storage in gigabytes."
    )
    "Disk storage in gigabytes."
    last_heartbeat: Optional[datetime] = Field(
        alias="lastHeartbeat", description="Last heartbeat timestamp from node agent."
    )
    "Last heartbeat timestamp from node agent."
    tags: Optional[Any] = Field(description="Custom tags for node organization.")
    "Custom tags for node organization."
    metadata: Optional[Any] = Field(description="Additional node metadata.")
    "Additional node metadata."
    infrastructure_node_created_at: Optional[datetime] = Field(
        alias="infrastructureNodeCreatedAt", description="When the node was registered."
    )
    "When the node was registered."
    updated_at: Optional[datetime] = Field(
        alias="updatedAt", description="When the node was last updated."
    )
    "When the node was last updated."
    live_state: Optional["InfrastructureNodeInNodeDefaultLiveState"] = Field(
        alias="liveState", description="Real-time state from the analytics data plane."
    )
    "Real-time state from the analytics data plane."
    effective_mode: NodeOperationalMode = Field(
        alias="effectiveMode",
        description="Current operational mode (normal / draining / maintenance). Read from\nFoghorn's per-node heartbeat. Changes via setNodeMode.",
    )
    "Current operational mode (normal / draining / maintenance). Read from\nFoghorn's per-node heartbeat. Changes via setNodeMode."
    routing_impact_preview: "InfrastructureNodeInNodeDefaultRoutingImpactPreview" = Field(
        alias="routingImpactPreview",
        description="Current traffic on this node. Operators use these counts to understand\nhow much live traffic is affected before draining or entering maintenance.\nCounts come from Foghorn's GetNodeHealth.",
    )
    "Current traffic on this node. Operators use these counts to understand\nhow much live traffic is affected before draining or entering maintenance.\nCounts come from Foghorn's GetNodeHealth."


class InfrastructureNodeInNodeDefaultLiveState(BaseModel):
    node_id: str = Field(alias="nodeId")
    tenant_id: str = Field(alias="tenantId")
    cpu_percent: float = Field(alias="cpuPercent")
    ram_used_bytes: float = Field(
        alias="ramUsedBytes",
        description="Used memory in MiB (field name is historical).",
    )
    "Used memory in MiB (field name is historical)."
    ram_total_bytes: float = Field(
        alias="ramTotalBytes",
        description="Total memory in MiB (field name is historical).",
    )
    "Total memory in MiB (field name is historical)."
    disk_used_bytes: float = Field(alias="diskUsedBytes")
    disk_total_bytes: float = Field(alias="diskTotalBytes")
    up_speed: float = Field(alias="upSpeed")
    down_speed: float = Field(alias="downSpeed")
    active_streams: int = Field(alias="activeStreams")
    is_healthy: bool = Field(alias="isHealthy")
    latitude: float
    longitude: float
    location: str
    metadata: Optional[Any]
    updated_at: datetime = Field(alias="updatedAt")


class InfrastructureNodeInNodeDefaultRoutingImpactPreview(BaseModel):
    """Snapshot of how much traffic a node is currently serving."""

    active_streams: int = Field(alias="activeStreams")
    active_viewers: int = Field(alias="activeViewers")


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


class InvoiceDefault(BaseModel):
    """A billing invoice for a subscription period.
    Includes base subscription and metered usage charges. Amounts are computed in
    EUR; a finalized invoice is presented and charged in the tenant's presentment
    currency at the ECB rate of its finalization date."""

    id: str = Field(description="Unique invoice identifier.")
    "Unique invoice identifier."
    amount: Any = Field(description="Total amount due in EUR (after credits applied).")
    "Total amount due in EUR (after credits applied)."
    base_amount: Any = Field(
        alias="baseAmount", description="Base subscription amount in EUR."
    )
    "Base subscription amount in EUR."
    metered_amount: Any = Field(
        alias="meteredAmount",
        description="Metered usage charges in EUR (net; 0 while usage is waived during beta).",
    )
    "Metered usage charges in EUR (net; 0 while usage is waived during beta)."
    gross_metered_amount: Any = Field(
        alias="grossMeteredAmount",
        description="Unwaived metered total — what usage would have cost. Equals meteredAmount when usage is not waived; the would-have-cost figure when it is.",
    )
    "Unwaived metered total — what usage would have cost. Equals meteredAmount when usage is not waived; the would-have-cost figure when it is."
    prepaid_credit_applied: Any = Field(
        alias="prepaidCreditApplied",
        description="Prepaid balance credit applied to this invoice, in EUR.",
    )
    "Prepaid balance credit applied to this invoice, in EUR."
    currency: Any = Field(description="Currency of the amounts above, EUR.")
    "Currency of the amounts above, EUR."
    presentment_amount_cents: Optional[int] = Field(
        alias="presentmentAmountCents",
        description="Total charged, in cents of `presentmentCurrency`. Null until the invoice is finalized.",
    )
    "Total charged, in cents of `presentmentCurrency`. Null until the invoice is finalized."
    presentment_currency: Optional[str] = Field(
        alias="presentmentCurrency",
        description="Currency the invoice is presented and charged in (EUR, USD, GBP). Empty until the invoice is finalized.",
    )
    "Currency the invoice is presented and charged in (EUR, USD, GBP). Empty until the invoice is finalized."
    presentment_units_per_eur: Optional[str] = Field(
        alias="presentmentUnitsPerEur",
        description="Units of `presentmentCurrency` one euro buys at finalization, as a decimal string. Empty until the invoice is finalized.",
    )
    "Units of `presentmentCurrency` one euro buys at finalization, as a decimal string. Empty until the invoice is finalized."
    presentment_reference_date: Optional[str] = Field(
        alias="presentmentReferenceDate",
        description="ECB reference date of the presentment rate (YYYY-MM-DD). Empty until the invoice is finalized.",
    )
    "ECB reference date of the presentment rate (YYYY-MM-DD). Empty until the invoice is finalized."
    finalized_at: Optional[datetime] = Field(
        alias="finalizedAt",
        description="When the invoice was finalized and its presentment amount fixed.",
    )
    "When the invoice was finalized and its presentment amount fixed."
    status: InvoiceStatus = Field(
        description="Invoice status (draft, open, paid, void)."
    )
    "Invoice status (draft, open, paid, void)."
    due_date: datetime = Field(alias="dueDate", description="Payment due date.")
    "Payment due date."
    paid_at: Optional[datetime] = Field(
        alias="paidAt", description="When payment was received."
    )
    "When payment was received."
    created_at: datetime = Field(
        alias="createdAt", description="When the invoice was created."
    )
    "When the invoice was created."
    updated_at: datetime = Field(
        alias="updatedAt", description="When the invoice was last updated."
    )
    "When the invoice was last updated."
    period_start: Optional[datetime] = Field(
        alias="periodStart", description="Billing period start date."
    )
    "Billing period start date."
    period_end: Optional[datetime] = Field(
        alias="periodEnd", description="Billing period end date."
    )
    "Billing period end date."
    usage_details: Optional[Any] = Field(
        alias="usageDetails", description="Detailed usage breakdown (JSON)."
    )
    "Detailed usage breakdown (JSON)."
    line_items: list["InvoiceDefaultLineItems"] = Field(
        alias="lineItems", description="Individual line items on the invoice."
    )
    "Individual line items on the invoice."


class InvoiceDefaultLineItems(BaseModel):
    """A single line item on an invoice or usage preview, produced by the rating engine.
    Decimal quantities are strings to preserve precision."""

    line_key: str = Field(
        alias="lineKey",
        description="Stable identity: 'base_subscription', 'meter:<name>:<cluster_id>:<yyyymm>'.",
    )
    "Stable identity: 'base_subscription', 'meter:<name>:<cluster_id>:<yyyymm>'."
    meter: str = Field(description="Meter name; empty for base_subscription.")
    "Meter name; empty for base_subscription."
    description: str = Field(description="Description of the charge.")
    "Description of the charge."
    quantity: str = Field(description="Total quantity used (decimal as string).")
    "Total quantity used (decimal as string)."
    included_quantity: str = Field(
        alias="includedQuantity", description="Free quantity (decimal as string)."
    )
    "Free quantity (decimal as string)."
    billable_quantity: str = Field(
        alias="billableQuantity", description="Billable quantity (decimal as string)."
    )
    "Billable quantity (decimal as string)."
    unit_price: str = Field(
        alias="unitPrice", description="Price per unit (decimal as string)."
    )
    "Price per unit (decimal as string)."
    total: str = Field(
        description="Total for this line item (= billableQuantity * unitPrice), decimal as string."
    )
    "Total for this line item (= billableQuantity * unitPrice), decimal as string."
    currency: str = Field(description="Currency (ISO 4217).")
    "Currency (ISO 4217)."
    cluster_id: Optional[str] = Field(
        alias="clusterId",
        description="Cluster id this line was attributed to; null for tenant-scoped lines (base_subscription).",
    )
    "Cluster id this line was attributed to; null for tenant-scoped lines (base_subscription)."
    cluster_name: Optional[str] = Field(
        alias="clusterName",
        description="Display name of the cluster, joined from Quartermaster at read time. Null when clusterId is null.",
    )
    "Display name of the cluster, joined from Quartermaster at read time. Null when clusterId is null."
    cluster_kind: Optional[str] = Field(
        alias="clusterKind",
        description="platform_official | tenant_private | third_party_marketplace; null when clusterId is null.",
    )
    "platform_official | tenant_private | third_party_marketplace; null when clusterId is null."
    pricing_source: str = Field(
        alias="pricingSource",
        description="Why the line was priced as it was: tier, cluster_metered, cluster_monthly, cluster_custom, free_unmetered, self_hosted, included_subscription.",
    )
    "Why the line was priced as it was: tier, cluster_metered, cluster_monthly, cluster_custom, free_unmetered, self_hosted, included_subscription."
    pricing_label: str = Field(
        alias="pricingLabel",
        description="Human-readable label of pricingSource (e.g. 'Self-hosted (no charge)', 'Marketplace metered'). Empty when not populated by the gateway.",
    )
    "Human-readable label of pricingSource (e.g. 'Self-hosted (no charge)', 'Marketplace metered'). Empty when not populated by the gateway."
    unit: str = Field(description="Canonical quantity unit.")
    "Canonical quantity unit."
    dimensions: Any = Field(
        description="Bounded pricing dimensions such as codec, rendition, backend, or model."
    )
    "Bounded pricing dimensions such as codec, rendition, backend, or model."


class InvoicePaymentDefault(BaseModel):
    """A tenant-visible invoice payment record. Provider secrets and raw payloads are never exposed."""

    id: str
    invoice_id: str = Field(alias="invoiceId")
    method: str
    amount: Any
    currency: Any
    status: str
    confirmed_at: Optional[datetime] = Field(alias="confirmedAt")
    created_at: datetime = Field(alias="createdAt")
    updated_at: datetime = Field(alias="updatedAt")
    conversion: Optional["InvoicePaymentDefaultConversion"] = Field(
        description="Amount charged and the EUR amount applied to the invoice, at the ECB rate of payment creation."
    )
    "Amount charged and the EUR amount applied to the invoice, at the ECB rate of payment creation."


class InvoicePaymentDefaultConversion(BaseModel):
    """Conversion of a payment into the EUR prepaid ledger and invoice currency.
    EUR amounts use the identity rate."""

    original_amount_cents: int = Field(
        alias="originalAmountCents",
        description="Amount charged, in cents of `originalCurrency`.",
    )
    "Amount charged, in cents of `originalCurrency`."
    original_currency: Any = Field(
        alias="originalCurrency", description="Currency charged (EUR, USD, GBP)."
    )
    "Currency charged (EUR, USD, GBP)."
    eur_amount_cents: int = Field(
        alias="eurAmountCents", description="EUR cents credited or applied."
    )
    "EUR cents credited or applied."
    units_per_eur: str = Field(
        alias="unitsPerEur",
        description='Units of `originalCurrency` one euro buys, as a decimal string ("1" for EUR).',
    )
    'Units of `originalCurrency` one euro buys, as a decimal string ("1" for EUR).'
    source: str = Field(
        description="Rate source: identity (EUR), ecb (ECB reference rate), or legacy_quote (USD rate quoted before ECB rates were stored)."
    )
    "Rate source: identity (EUR), ecb (ECB reference rate), or legacy_quote (USD rate quoted before ECB rates were stored)."
    reference_date: str = Field(
        alias="referenceDate",
        description="ECB reference date of the rate (YYYY-MM-DD).",
    )
    "ECB reference date of the rate (YYYY-MM-DD)."


class LinkEmailPayloadDefault(BaseModel):
    """Successful email link response."""

    success: bool = Field(description="Whether the operation succeeded.")
    "Whether the operation succeeded."
    message: str = Field(description="Human-readable status message.")
    "Human-readable status message."
    verification_sent: bool = Field(
        alias="verificationSent", description="Whether a verification email was sent."
    )
    "Whether a verification email was sent."


class MarketplaceClusterDefault(BaseModel):
    cluster_id: str = Field(alias="clusterId")
    cluster_name: str = Field(alias="clusterName")
    short_description: Optional[str] = Field(alias="shortDescription")
    visibility: ClusterVisibility
    pricing_model: ClusterPricingModel = Field(alias="pricingModel")
    monthly_price_cents: Optional[int] = Field(alias="monthlyPriceCents")
    requires_approval: bool = Field(alias="requiresApproval")
    owner_name: Optional[str] = Field(alias="ownerName")
    max_concurrent_streams: int = Field(alias="maxConcurrentStreams")
    max_concurrent_viewers: int = Field(alias="maxConcurrentViewers")
    current_utilization: Optional[float] = Field(alias="currentUtilization")
    is_subscribed: bool = Field(alias="isSubscribed")
    subscription_status: Optional[ClusterSubscriptionStatus] = Field(
        alias="subscriptionStatus"
    )
    is_eligible: bool = Field(
        alias="isEligible",
        description="Whether the tenant can subscribe based on billing tier.",
    )
    "Whether the tenant can subscribe based on billing tier."
    denial_reason: Optional[str] = Field(
        alias="denialReason",
        description="Generic reason if the tenant is not eligible.",
    )
    "Generic reason if the tenant is not eligible."


class MediaCapacityConsentChangeDefault(BaseModel):
    cluster_id: str = Field(alias="clusterId")
    idempotency_key: str = Field(alias="idempotencyKey")
    revision: str
    digest: str
    rollout: "MediaCapacityConsentChangeDefaultRollout"
    created_at: datetime = Field(alias="createdAt")


class MediaCapacityConsentChangeDefaultRollout(BaseModel):
    status: MediaPlacementRolloutStatus
    required_recipients: int = Field(alias="requiredRecipients")
    applied_recipients: int = Field(alias="appliedRecipients")
    pending_recipients: list[
        "MediaCapacityConsentChangeDefaultRolloutPendingRecipients"
    ] = Field(alias="pendingRecipients")
    existing_sessions_retained: bool = Field(alias="existingSessionsRetained")
    updated_at: Optional[datetime] = Field(alias="updatedAt")


class MediaCapacityConsentChangeDefaultRolloutPendingRecipients(BaseModel):
    id: str
    name: str
    status: MediaPlacementRolloutStatus
    reason: Optional[str]
    authority_expires_at: Optional[datetime] = Field(alias="authorityExpiresAt")


class MediaCapacityConsentDefault(BaseModel):
    cluster_id: str = Field(alias="clusterId")
    revision: str
    allow_ingest: bool = Field(alias="allowIngest")
    allow_serve: bool = Field(alias="allowServe")
    allow_external_source: bool = Field(alias="allowExternalSource")
    can_manage: bool = Field(alias="canManage")
    rollout: "MediaCapacityConsentDefaultRollout"


class MediaCapacityConsentDefaultRollout(BaseModel):
    status: MediaPlacementRolloutStatus
    required_recipients: int = Field(alias="requiredRecipients")
    applied_recipients: int = Field(alias="appliedRecipients")
    pending_recipients: list["MediaCapacityConsentDefaultRolloutPendingRecipients"] = (
        Field(alias="pendingRecipients")
    )
    existing_sessions_retained: bool = Field(alias="existingSessionsRetained")
    updated_at: Optional[datetime] = Field(alias="updatedAt")


class MediaCapacityConsentDefaultRolloutPendingRecipients(BaseModel):
    id: str
    name: str
    status: MediaPlacementRolloutStatus
    reason: Optional[str]
    authority_expires_at: Optional[datetime] = Field(alias="authorityExpiresAt")


class MediaPlacementChangeDefault(BaseModel):
    scope: "MediaPlacementChangeDefaultScope"
    idempotency_key: str = Field(alias="idempotencyKey")
    revision: str
    parent_revision: str = Field(alias="parentRevision")
    digest: str
    rollout: "MediaPlacementChangeDefaultRollout"
    created_at: datetime = Field(alias="createdAt")


class MediaPlacementChangeDefaultScope(BaseModel):
    kind: MediaPlacementScopeKind
    stream_id: Optional[str] = Field(alias="streamId")


class MediaPlacementChangeDefaultRollout(BaseModel):
    status: MediaPlacementRolloutStatus
    required_recipients: int = Field(alias="requiredRecipients")
    applied_recipients: int = Field(alias="appliedRecipients")
    pending_recipients: list["MediaPlacementChangeDefaultRolloutPendingRecipients"] = (
        Field(alias="pendingRecipients")
    )
    existing_sessions_retained: bool = Field(alias="existingSessionsRetained")
    updated_at: Optional[datetime] = Field(alias="updatedAt")


class MediaPlacementChangeDefaultRolloutPendingRecipients(BaseModel):
    id: str
    name: str
    status: MediaPlacementRolloutStatus
    reason: Optional[str]
    authority_expires_at: Optional[datetime] = Field(alias="authorityExpiresAt")


class MediaPlacementErrorInMediaCapacityConsentChangeResultDefault(BaseModel):
    media_placement_error_code: MediaPlacementErrorCode = Field(
        alias="mediaPlacementErrorCode"
    )
    message: str
    fields: list["MediaPlacementErrorInMediaCapacityConsentChangeResultDefaultFields"]
    current_revision: Optional[str] = Field(alias="currentRevision")
    parent_revision: Optional[str] = Field(alias="parentRevision")
    retry_after_seconds: Optional[int] = Field(alias="retryAfterSeconds")


class MediaPlacementErrorInMediaCapacityConsentChangeResultDefaultFields(BaseModel):
    path: str
    group_id: Optional[str] = Field(alias="groupId")
    message: str


class MediaPlacementErrorInMediaCapacityConsentResultDefault(BaseModel):
    media_placement_error_code: MediaPlacementErrorCode = Field(
        alias="mediaPlacementErrorCode"
    )
    message: str
    fields: list["MediaPlacementErrorInMediaCapacityConsentResultDefaultFields"]
    current_revision: Optional[str] = Field(alias="currentRevision")
    parent_revision: Optional[str] = Field(alias="parentRevision")
    retry_after_seconds: Optional[int] = Field(alias="retryAfterSeconds")


class MediaPlacementErrorInMediaCapacityConsentResultDefaultFields(BaseModel):
    path: str
    group_id: Optional[str] = Field(alias="groupId")
    message: str


class MediaPlacementErrorInMediaPlacementChangeResultDefault(BaseModel):
    media_placement_error_code: MediaPlacementErrorCode = Field(
        alias="mediaPlacementErrorCode"
    )
    message: str
    fields: list["MediaPlacementErrorInMediaPlacementChangeResultDefaultFields"]
    current_revision: Optional[str] = Field(alias="currentRevision")
    media_placement_error_parent_revision: Optional[str] = Field(
        alias="mediaPlacementErrorParentRevision"
    )
    retry_after_seconds: Optional[int] = Field(alias="retryAfterSeconds")


class MediaPlacementErrorInMediaPlacementChangeResultDefaultFields(BaseModel):
    path: str
    group_id: Optional[str] = Field(alias="groupId")
    message: str


class MediaPlacementErrorInMediaPlacementOptionsResultDefault(BaseModel):
    media_placement_error_code: MediaPlacementErrorCode = Field(
        alias="mediaPlacementErrorCode"
    )
    message: str
    fields: list["MediaPlacementErrorInMediaPlacementOptionsResultDefaultFields"]
    current_revision: Optional[str] = Field(alias="currentRevision")
    parent_revision: Optional[str] = Field(alias="parentRevision")
    retry_after_seconds: Optional[int] = Field(alias="retryAfterSeconds")


class MediaPlacementErrorInMediaPlacementOptionsResultDefaultFields(BaseModel):
    path: str
    group_id: Optional[str] = Field(alias="groupId")
    message: str


class MediaPlacementErrorInMediaPlacementPolicyResultDefault(BaseModel):
    media_placement_error_code: MediaPlacementErrorCode = Field(
        alias="mediaPlacementErrorCode"
    )
    message: str
    fields: list["MediaPlacementErrorInMediaPlacementPolicyResultDefaultFields"]
    current_revision: Optional[str] = Field(alias="currentRevision")
    media_placement_error_parent_revision: Optional[str] = Field(
        alias="mediaPlacementErrorParentRevision"
    )
    retry_after_seconds: Optional[int] = Field(alias="retryAfterSeconds")


class MediaPlacementErrorInMediaPlacementPolicyResultDefaultFields(BaseModel):
    path: str
    group_id: Optional[str] = Field(alias="groupId")
    message: str


class MediaPlacementErrorInMediaPlacementPreviewResultDefault(BaseModel):
    media_placement_error_code: MediaPlacementErrorCode = Field(
        alias="mediaPlacementErrorCode"
    )
    message: str
    fields: list["MediaPlacementErrorInMediaPlacementPreviewResultDefaultFields"]
    current_revision: Optional[str] = Field(alias="currentRevision")
    media_placement_error_parent_revision: Optional[str] = Field(
        alias="mediaPlacementErrorParentRevision"
    )
    retry_after_seconds: Optional[int] = Field(alias="retryAfterSeconds")


class MediaPlacementErrorInMediaPlacementPreviewResultDefaultFields(BaseModel):
    path: str
    group_id: Optional[str] = Field(alias="groupId")
    message: str


class MediaPlacementErrorInMediaPlacementReviewResultDefault(BaseModel):
    media_placement_error_code: MediaPlacementErrorCode = Field(
        alias="mediaPlacementErrorCode"
    )
    message: str
    fields: list["MediaPlacementErrorInMediaPlacementReviewResultDefaultFields"]
    current_revision: Optional[str] = Field(alias="currentRevision")
    parent_revision: Optional[str] = Field(alias="parentRevision")
    retry_after_seconds: Optional[int] = Field(alias="retryAfterSeconds")


class MediaPlacementErrorInMediaPlacementReviewResultDefaultFields(BaseModel):
    path: str
    group_id: Optional[str] = Field(alias="groupId")
    message: str


class MediaPlacementOptionsConnectionDefault(BaseModel):
    nodes: list["MediaPlacementOptionsConnectionDefaultNodes"]
    page_info: "MediaPlacementOptionsConnectionDefaultPageInfo" = Field(
        alias="pageInfo"
    )


class MediaPlacementOptionsConnectionDefaultNodes(BaseModel):
    id: str
    name: str
    kind: MediaPlacementOptionKind
    cluster_class: Optional[MediaPlacementClass] = Field(alias="clusterClass")
    region: Optional[str]
    owner_id: Optional[str] = Field(alias="ownerId")
    cluster_id: Optional[str] = Field(
        alias="clusterId", description="Cluster of a NODE option."
    )
    "Cluster of a NODE option."
    eligible: bool
    reason: Optional[str]


class MediaPlacementOptionsConnectionDefaultPageInfo(BaseModel):
    start_cursor: Optional[str] = Field(alias="startCursor")
    end_cursor: Optional[str] = Field(alias="endCursor")
    has_next_page: bool = Field(alias="hasNextPage")
    has_previous_page: bool = Field(alias="hasPreviousPage")


class MediaPlacementPolicyStateDefault(BaseModel):
    scope: "MediaPlacementPolicyStateDefaultScope"
    revision: str
    parent_revision: str = Field(alias="parentRevision")
    active_revision: Optional[str] = Field(alias="activeRevision")
    active_parent_revision: Optional[str] = Field(alias="activeParentRevision")
    verbs: list["MediaPlacementPolicyStateDefaultVerbs"]
    rollout: "MediaPlacementPolicyStateDefaultRollout"
    actions: "MediaPlacementPolicyStateDefaultActions"
    features: "MediaPlacementPolicyStateDefaultFeatures"


class MediaPlacementPolicyStateDefaultScope(BaseModel):
    kind: MediaPlacementScopeKind
    stream_id: Optional[str] = Field(alias="streamId")


class MediaPlacementPolicyStateDefaultVerbs(BaseModel):
    verb: MediaPlacementVerb
    own_rules: Optional["MediaPlacementPolicyStateDefaultVerbsOwnRules"] = Field(
        alias="ownRules"
    )
    inherited_rules: Optional["MediaPlacementPolicyStateDefaultVerbsInheritedRules"] = (
        Field(alias="inheritedRules")
    )
    requested_effective: "MediaPlacementPolicyStateDefaultVerbsRequestedEffective" = Field(
        alias="requestedEffective",
        description="Compiled requested intent; enforcement progress is reported separately.",
    )
    "Compiled requested intent; enforcement progress is reported separately."


class MediaPlacementPolicyStateDefaultVerbsOwnRules(BaseModel):
    schema_version: int = Field(alias="schemaVersion")


class MediaPlacementPolicyStateDefaultVerbsInheritedRules(BaseModel):
    schema_version: int = Field(alias="schemaVersion")


class MediaPlacementPolicyStateDefaultVerbsRequestedEffective(BaseModel):
    schema_version: int = Field(alias="schemaVersion")
    digest: str


class MediaPlacementPolicyStateDefaultRollout(BaseModel):
    status: MediaPlacementRolloutStatus
    required_recipients: int = Field(alias="requiredRecipients")
    applied_recipients: int = Field(alias="appliedRecipients")
    pending_recipients: list[
        "MediaPlacementPolicyStateDefaultRolloutPendingRecipients"
    ] = Field(alias="pendingRecipients")
    existing_sessions_retained: bool = Field(alias="existingSessionsRetained")
    updated_at: Optional[datetime] = Field(alias="updatedAt")


class MediaPlacementPolicyStateDefaultRolloutPendingRecipients(BaseModel):
    id: str
    name: str
    status: MediaPlacementRolloutStatus
    reason: Optional[str]
    authority_expires_at: Optional[datetime] = Field(alias="authorityExpiresAt")


class MediaPlacementPolicyStateDefaultActions(BaseModel):
    can_read: bool = Field(alias="canRead")
    can_preview: bool = Field(alias="canPreview")
    can_manage: bool = Field(alias="canManage")
    can_inspect_private_candidates: bool = Field(alias="canInspectPrivateCandidates")


class MediaPlacementPolicyStateDefaultFeatures(BaseModel):
    schema_version: int = Field(alias="schemaVersion")
    geographic_spillover: bool = Field(alias="geographicSpillover")
    price_ordering: bool = Field(alias="priceOrdering")
    supported_presets: list[str] = Field(alias="supportedPresets")


class MediaPlacementPreviewDefault(BaseModel):
    scope: "MediaPlacementPreviewDefaultScope"
    verb: MediaPlacementVerb
    revision: str
    parent_revision: str = Field(alias="parentRevision")
    digest: str
    reason: str
    selected: Optional["MediaPlacementPreviewDefaultSelected"]
    candidates: list["MediaPlacementPreviewDefaultCandidates"]
    transitions: list["MediaPlacementPreviewDefaultTransitions"]
    observed_at: datetime = Field(alias="observedAt")
    expires_at: datetime = Field(alias="expiresAt")
    complete: bool
    source_evaluated: bool = Field(alias="sourceEvaluated")
    active_ingest_cluster_id: Optional[str] = Field(alias="activeIngestClusterId")


class MediaPlacementPreviewDefaultScope(BaseModel):
    kind: MediaPlacementScopeKind
    stream_id: Optional[str] = Field(alias="streamId")


class MediaPlacementPreviewDefaultSelected(BaseModel):
    cluster_id: str = Field(alias="clusterId")
    cluster_name: str = Field(alias="clusterName")
    region: Optional[str]
    node_id: Optional[str] = Field(
        alias="nodeId",
        description="Only returned when private candidate inspection is authorized.",
    )
    "Only returned when private candidate inspection is authorized."
    group_id: Optional[str] = Field(alias="groupId")
    reason: str
    distance_km: Optional[float] = Field(alias="distanceKm")
    requires_source_pull: bool = Field(alias="requiresSourcePull")
    price: Optional["MediaPlacementPreviewDefaultSelectedPrice"]


class MediaPlacementPreviewDefaultSelectedPrice(BaseModel):
    amount_micros: str = Field(alias="amountMicros")
    currency: str
    unit: str
    revision: str
    expires_at: datetime = Field(alias="expiresAt")


class MediaPlacementPreviewDefaultCandidates(BaseModel):
    cluster_id: str = Field(alias="clusterId")
    cluster_name: str = Field(alias="clusterName")
    region: Optional[str]
    node_id: Optional[str] = Field(
        alias="nodeId",
        description="Only returned when private candidate inspection is authorized.",
    )
    "Only returned when private candidate inspection is authorized."
    group_id: Optional[str] = Field(alias="groupId")
    reason: str
    distance_km: Optional[float] = Field(alias="distanceKm")
    requires_source_pull: bool = Field(alias="requiresSourcePull")
    price: Optional["MediaPlacementPreviewDefaultCandidatesPrice"]


class MediaPlacementPreviewDefaultCandidatesPrice(BaseModel):
    amount_micros: str = Field(alias="amountMicros")
    currency: str
    unit: str
    revision: str
    expires_at: datetime = Field(alias="expiresAt")


class MediaPlacementPreviewDefaultTransitions(BaseModel):
    from_group: str = Field(alias="fromGroup")
    reason: str


class MediaPlacementReviewDefault(BaseModel):
    review_token: str = Field(alias="reviewToken")
    digest: str
    expires_at: datetime = Field(alias="expiresAt")
    differences: list["MediaPlacementReviewDefaultDifferences"]
    warnings: list["MediaPlacementReviewDefaultWarnings"]
    impact: "MediaPlacementReviewDefaultImpact"


class MediaPlacementReviewDefaultDifferences(BaseModel):
    path: str
    label: str
    before: str
    after: str


class MediaPlacementReviewDefaultWarnings(BaseModel):
    id: str
    severity: MediaPlacementWarningSeverity
    message: str
    acknowledgement_required: bool = Field(alias="acknowledgementRequired")


class MediaPlacementReviewDefaultImpact(BaseModel):
    affected_streams: int = Field(alias="affectedStreams")
    active_publishers: int = Field(alias="activePublishers")
    complete: bool
    existing_sessions_retained: bool = Field(alias="existingSessionsRetained")


class MediaRetentionPolicyDefault(BaseModel):
    """Tenant-default retention policy: per-class overrides + the values the
    cascade would resolve to today for a hypothetical new artifact of each
    class (no per-stream context)."""

    bounds: "MediaRetentionPolicyDefaultBounds"
    updated_by: Optional[str] = Field(
        alias="updatedBy",
        description="User who last touched the policy. Null when no tenant override is set.",
    )
    "User who last touched the policy. Null when no tenant override is set."
    updated_at: Optional[datetime] = Field(alias="updatedAt")
    default_vod_retention_days: Optional[int] = Field(
        alias="defaultVodRetentionDays",
        description="Per-class tenant default for VOD uploads. Null = inherit the system\ndefault (keep forever). 0 = no auto-expire (paid-tier baseline). Free\ntier clamps to the tier cap at write time.",
    )
    "Per-class tenant default for VOD uploads. Null = inherit the system\ndefault (keep forever). 0 = no auto-expire (paid-tier baseline). Free\ntier clamps to the tier cap at write time."
    default_dvr_retention_days: Optional[int] = Field(
        alias="defaultDvrRetentionDays",
        description="Per-class tenant default for DVR recordings. Null = inherit the system\ndefault (30d). 0 = no auto-expire (paid only).",
    )
    "Per-class tenant default for DVR recordings. Null = inherit the system\ndefault (30d). 0 = no auto-expire (paid only)."
    default_clip_retention_days: Optional[int] = Field(
        alias="defaultClipRetentionDays",
        description="Per-class tenant default for clips. Null = inherit the system default\n(30d). 0 = no auto-expire (paid only).",
    )
    "Per-class tenant default for clips. Null = inherit the system default\n(30d). 0 = no auto-expire (paid only)."
    effective_vod_retention_days: int = Field(
        alias="effectiveVodRetentionDays",
        description="Effective VOD horizon the cascade resolves to today (0 = keep forever).",
    )
    "Effective VOD horizon the cascade resolves to today (0 = keep forever)."
    effective_dvr_retention_days: int = Field(
        alias="effectiveDvrRetentionDays",
        description="Effective DVR horizon the cascade resolves to today (0 = keep forever).",
    )
    "Effective DVR horizon the cascade resolves to today (0 = keep forever)."
    effective_clip_retention_days: int = Field(
        alias="effectiveClipRetentionDays",
        description="Effective clip horizon the cascade resolves to today (0 = keep forever).",
    )
    "Effective clip horizon the cascade resolves to today (0 = keep forever)."


class MediaRetentionPolicyDefaultBounds(BaseModel):
    """Upper bounds for customer-set media retention, derived from the tenant's
    tier entitlement at request time."""

    max_recording_retention_days: int = Field(
        alias="maxRecordingRetentionDays",
        description="Maximum retention days the tenant's tier permits. 0 = no cap (paid-tier\nbaseline); Free tier sets a finite cap as the anti-abuse guardrail.",
    )
    "Maximum retention days the tenant's tier permits. 0 = no cap (paid-tier\nbaseline); Free tier sets a finite cap as the anti-abuse guardrail."


class MessageDefault(BaseModel):
    """A message within a support conversation."""

    id: str = Field(description="The globally unique identifier for this message.")
    "The globally unique identifier for this message."
    conversation_id: str = Field(
        alias="conversationId", description="The conversation this message belongs to."
    )
    "The conversation this message belongs to."
    content: str = Field(description="The message content.")
    "The message content."
    sender: MessageSender = Field(description="Who sent this message.")
    "Who sent this message."
    created_at: datetime = Field(
        alias="createdAt", description="When the message was sent."
    )
    "When the message was sent."


class MistAdminSessionDefault(BaseModel):
    """Short-lived credentials that let an authorized operator open the
    MistServer admin UI on a specific edge node.

    The webapp POSTs `sessionToken` (in the form body, NOT a query string)
    to `postUrl`. Helmsman validates the token against Foghorn, sets an
    HttpOnly, Secure, SameSite=Lax cookie scoped to `Path=/_mist`, and
    redirects the browser to `/_mist/` where the LSP UI loads.

    `sessionToken` is bound to one nodeId via the JWT's `node_id` claim;
    replay against any other edge node fails. `expiresAt` is Unix seconds."""

    post_url: str = Field(
        alias="postUrl",
        description="Per-edge URL to POST the session token to (e.g. https://edge-us-1.media-us-1.frameworks.network/_mist-session).",
    )
    "Per-edge URL to POST the session token to (e.g. https://edge-us-1.media-us-1.frameworks.network/_mist-session)."
    session_token: str = Field(
        alias="sessionToken",
        description="Short-TTL JWT to submit as the `session_token` form field. Treat as a secret.",
    )
    "Short-TTL JWT to submit as the `session_token` form field. Treat as a secret."
    expires_at: int = Field(
        alias="expiresAt",
        description="Unix-seconds expiry of the token. The webapp can refuse to open if already past.",
    )
    "Unix-seconds expiry of the token. The webapp can refuse to open if already past."


class MollieFirstPaymentDefault(BaseModel):
    """Mollie First Payment - redirect URL for initial payment that creates a mandate."""

    payment_id: str = Field(
        alias="paymentId", description="Mollie payment ID (tr_xxx)."
    )
    "Mollie payment ID (tr_xxx)."
    customer_id: str = Field(
        alias="customerId", description="Mollie customer ID (cst_xxx)."
    )
    "Mollie customer ID (cst_xxx)."
    payment_url: str = Field(
        alias="paymentUrl", description="URL to redirect user to for payment."
    )
    "URL to redirect user to for payment."


class MollieMandateDefault(BaseModel):
    """Mollie Mandate - recurring payment authorization for a customer."""

    mandate_id: str = Field(
        alias="mandateId", description="Mollie mandate ID (mdt_xxx)."
    )
    "Mollie mandate ID (mdt_xxx)."
    customer_id: str = Field(
        alias="customerId", description="Mollie customer ID (cst_xxx)."
    )
    "Mollie customer ID (cst_xxx)."
    status: str = Field(
        description="Mandate status (valid, pending, invalid, revoked)."
    )
    "Mandate status (valid, pending, invalid, revoked)."
    method: str = Field(description="Mandate method (directdebit, creditcard, ideal).")
    "Mandate method (directdebit, creditcard, ideal)."
    details: Optional[Any] = Field(description="Mandate details (bank/card info).")
    "Mandate details (bank/card info)."
    created_at: Optional[datetime] = Field(
        alias="createdAt", description="Mandate creation timestamp."
    )
    "Mandate creation timestamp."


class MollieSubscriptionDefault(BaseModel):
    """Mollie Subscription - recurring subscription created after mandate is valid."""

    subscription_id: str = Field(
        alias="subscriptionId", description="Mollie subscription ID (sub_xxx)."
    )
    "Mollie subscription ID (sub_xxx)."
    status: str = Field(description="Subscription status (active, pending, etc.).")
    "Subscription status (active, pending, etc.)."
    next_payment_date: Optional[str] = Field(
        alias="nextPaymentDate", description="Next payment date (ISO 8601)."
    )
    "Next payment date (ISO 8601)."


class NetworkStatusDefault(BaseModel):
    clusters: list["NetworkStatusDefaultClusters"]
    peer_connections: list["NetworkStatusDefaultPeerConnections"] = Field(
        alias="peerConnections"
    )
    nodes: list["NetworkStatusDefaultNodes"]
    service_instances: list["NetworkStatusDefaultServiceInstances"] = Field(
        alias="serviceInstances"
    )
    total_nodes: int = Field(alias="totalNodes")
    healthy_nodes: int = Field(alias="healthyNodes")
    updated_at: datetime = Field(alias="updatedAt")


class NetworkStatusDefaultClusters(BaseModel):
    cluster_id: str = Field(alias="clusterId")
    name: str
    region: str
    latitude: float
    longitude: float
    node_count: int = Field(alias="nodeCount")
    healthy_node_count: int = Field(alias="healthyNodeCount")
    peer_count: int = Field(alias="peerCount")
    status: str
    cluster_type: str = Field(alias="clusterType")
    short_description: Optional[str] = Field(alias="shortDescription")
    current_streams: int = Field(alias="currentStreams")
    current_viewers: int = Field(alias="currentViewers")
    egress_mbps: int = Field(alias="egressMbps")
    egress_capacity_mbps: int = Field(alias="egressCapacityMbps")
    ingress_mbps: int = Field(alias="ingressMbps")
    services: list[str]


class NetworkStatusDefaultPeerConnections(BaseModel):
    source_cluster: str = Field(alias="sourceCluster")
    target_cluster: str = Field(alias="targetCluster")
    connected: bool
    connection_type: str = Field(alias="connectionType")


class NetworkStatusDefaultNodes(BaseModel):
    node_id: str = Field(alias="nodeId")
    name: str
    node_type: str = Field(alias="nodeType")
    latitude: float
    longitude: float
    status: str
    cluster_id: str = Field(alias="clusterId")


class NetworkStatusDefaultServiceInstances(BaseModel):
    instance_id: str = Field(alias="instanceId")
    service_id: str = Field(alias="serviceId")
    cluster_id: str = Field(alias="clusterId")
    node_id: Optional[str] = Field(alias="nodeId")
    status: str
    health_status: str = Field(alias="healthStatus")


class NodeMetricDefault(BaseModel):
    id: str
    timestamp: datetime
    node_id: str = Field(alias="nodeId")
    cluster_id: Optional[str] = Field(alias="clusterId")
    cpu_usage: float = Field(alias="cpuUsage")
    memory_total: Optional[float] = Field(
        alias="memoryTotal", description="Total memory in MiB."
    )
    "Total memory in MiB."
    memory_used: Optional[float] = Field(
        alias="memoryUsed", description="Used memory in MiB."
    )
    "Used memory in MiB."
    disk_total: Optional[float] = Field(
        alias="diskTotal", description="Total disk capacity in bytes."
    )
    "Total disk capacity in bytes."
    disk_used: Optional[float] = Field(
        alias="diskUsed", description="Used disk capacity in bytes."
    )
    "Used disk capacity in bytes."
    shm_total: Optional[float] = Field(
        alias="shmTotal", description="Total shared memory in bytes."
    )
    "Total shared memory in bytes."
    shm_used: Optional[float] = Field(
        alias="shmUsed", description="Used shared memory in bytes."
    )
    "Used shared memory in bytes."
    network_rx: float = Field(
        alias="networkRx",
        description="Cumulative bytes received (resets on node restart).",
    )
    "Cumulative bytes received (resets on node restart)."
    network_tx: float = Field(
        alias="networkTx", description="Cumulative bytes sent (resets on node restart)."
    )
    "Cumulative bytes sent (resets on node restart)."
    up_speed: Optional[float] = Field(
        alias="upSpeed", description="Upload throughput in bytes/sec."
    )
    "Upload throughput in bytes/sec."
    down_speed: Optional[float] = Field(
        alias="downSpeed", description="Download throughput in bytes/sec."
    )
    "Download throughput in bytes/sec."
    connections_current: Optional[int] = Field(alias="connectionsCurrent")
    stream_count: Optional[int] = Field(alias="streamCount")
    status: str
    is_healthy: Optional[bool] = Field(alias="isHealthy")
    latitude: Optional[float]
    longitude: Optional[float]
    metadata: Optional[Any]


class NodeMetricHourlyDefault(BaseModel):
    id: str
    timestamp: datetime
    node_id: str = Field(alias="nodeId")
    cluster_id: Optional[str] = Field(alias="clusterId")
    avg_cpu: float = Field(alias="avgCpu")
    peak_cpu: float = Field(alias="peakCpu")
    avg_memory: float = Field(alias="avgMemory")
    peak_memory: float = Field(alias="peakMemory")
    avg_disk: float = Field(alias="avgDisk")
    peak_disk: float = Field(alias="peakDisk")
    avg_shm: float = Field(alias="avgShm")
    peak_shm: float = Field(alias="peakShm")
    total_bandwidth_in: float = Field(alias="totalBandwidthIn")
    total_bandwidth_out: float = Field(alias="totalBandwidthOut")
    was_healthy: bool = Field(alias="wasHealthy")


class NodeMetricsAggregatedDefault(BaseModel):
    node_id: str = Field(alias="nodeId")
    cluster_id: Optional[str] = Field(alias="clusterId")
    avg_cpu: float = Field(alias="avgCpu")
    avg_memory: float = Field(alias="avgMemory")
    avg_disk: float = Field(alias="avgDisk")
    avg_shm: float = Field(alias="avgShm")
    total_bandwidth_in: float = Field(alias="totalBandwidthIn")
    total_bandwidth_out: float = Field(alias="totalBandwidthOut")
    sample_count: int = Field(alias="sampleCount")


class NodePerformance5mDefault(BaseModel):
    id: str
    timestamp: datetime
    node_id: str = Field(alias="nodeId")
    avg_cpu: float = Field(alias="avgCpu")
    max_cpu: float = Field(alias="maxCpu")
    avg_memory: float = Field(alias="avgMemory")
    max_memory: float = Field(alias="maxMemory")
    total_bandwidth: float = Field(alias="totalBandwidth")
    avg_streams: int = Field(alias="avgStreams")
    max_streams: int = Field(alias="maxStreams")


class NotFoundErrorDefault(BaseModel):
    message: str
    code: Optional[str]
    resource_type: str = Field(alias="resourceType")
    resource_id: str = Field(alias="resourceId")


class NotFoundError(BaseModel):
    typename__: str = Field(alias="__typename")
    message: str
    code: Optional[str]
    resource_type: str = Field(alias="resourceType")
    resource_id: str = Field(alias="resourceId")


class OrchestratorInstanceDefault(BaseModel):
    """Per-instance state. One per (tenant, orch_addr, resolved_ip). Multiple
    instances of the same orch can have independent prices, capabilities, and
    hardware — the side panel renders this list so divergence is visible."""

    tenant_id: str = Field(alias="tenantId")
    orch_addr: str = Field(alias="orchAddr")
    resolved_ip: str = Field(alias="resolvedIp")
    canonical_url: str = Field(alias="canonicalUrl")
    advertised_node_urls: list[str] = Field(alias="advertisedNodeUrls")
    capabilities: list[str]
    price_per_unit_eth: str = Field(alias="pricePerUnitEth")
    pixels_per_unit: str = Field(alias="pixelsPerUnit")
    capability_prices: list["OrchestratorInstanceDefaultCapabilityPrices"] = Field(
        alias="capabilityPrices"
    )
    hardware: str
    source: str
    last_seen: datetime = Field(alias="lastSeen")
    updated_at: datetime = Field(alias="updatedAt")


class OrchestratorInstanceDefaultCapabilityPrices(BaseModel):
    """One per-capability price entry. The gateway emits these as typed
    `OrchestratorCapabilityPriceEntry` proto messages; if `capability` is empty
    it falls back to `pos:<N>` reflecting the index in upstream's
    `capabilities_prices` array. Prices are exposed as ETH-denominated decimal
    strings so clients never handle wei-scale integers through JSON floats."""

    capability: str
    price_per_unit_eth: str = Field(alias="pricePerUnitEth")
    pixels_per_unit: str = Field(alias="pixelsPerUnit")


class OrchestratorPerformancePointDefault(BaseModel):
    """One sample from discovery and outcome rollups. Discovery metrics are
    per-vantage; transcode and AI outcome metrics are keyed by the gateway and the
    resolved instance IP observed by the gateway."""

    timestamp: datetime
    gateway_id: str = Field(alias="gatewayId")
    gateway_region: str = Field(alias="gatewayRegion")
    resolved_ip: str = Field(alias="resolvedIp")
    attempts: float
    successes: float
    failures: float
    mean_latency_ms: float = Field(alias="meanLatencyMs")
    max_latency_ms: int = Field(alias="maxLatencyMs")
    transcode_attempts: float = Field(alias="transcodeAttempts")
    transcode_successes: float = Field(alias="transcodeSuccesses")
    transcode_failures: float = Field(alias="transcodeFailures")
    transcode_mean_overall_ms: float = Field(alias="transcodeMeanOverallMs")
    transcode_max_overall_ms: int = Field(alias="transcodeMaxOverallMs")
    transcode_pixels: float = Field(alias="transcodePixels")
    ai_attempts: float = Field(alias="aiAttempts")
    ai_successes: float = Field(alias="aiSuccesses")
    ai_failures: float = Field(alias="aiFailures")
    ai_mean_latency_ms: float = Field(alias="aiMeanLatencyMs")
    ai_max_latency_ms: int = Field(alias="aiMaxLatencyMs")


class OrchestratorVantageDefault(BaseModel):
    """Per-vantage observation: one row per (cluster owner tenant, gateway, orch
    address, resolved IP). DNS round-robin / geo-anycast surfaces as multiple
    vantages with different `resolvedIp`."""

    tenant_id: str = Field(alias="tenantId")
    gateway_id: str = Field(alias="gatewayId")
    gateway_region: str = Field(alias="gatewayRegion")
    orch_addr: str = Field(alias="orchAddr")
    resolved_ip: str = Field(alias="resolvedIp")
    latitude: float
    longitude: float
    city: str
    country_code: str = Field(alias="countryCode")
    geo_source: str = Field(alias="geoSource")
    geo_resolved_at: Optional[datetime] = Field(
        alias="geoResolvedAt",
        description="When the vantage's location was resolved; null while it has not been.",
    )
    "When the vantage's location was resolved; null while it has not been."
    latest_latency_ms: int = Field(alias="latestLatencyMs")
    score: float
    dialed_recently: bool = Field(alias="dialedRecently")
    last_seen: datetime = Field(alias="lastSeen")


class OrchestratorWithDetailsDefault(BaseModel):
    """Detail response: orchestrator identity + every known instance (with their
    own price/capabilities/hardware) + every per-(gateway, instance) vantage.
    Used by the federation map's side panel."""

    orchestrator: "OrchestratorWithDetailsDefaultOrchestrator"
    instances: list["OrchestratorWithDetailsDefaultInstances"]
    vantages: list["OrchestratorWithDetailsDefaultVantages"]


class OrchestratorWithDetailsDefaultOrchestrator(BaseModel):
    """Identity-level orchestrator row — keyed by eth address. An orchestrator
    typically fronts N instances behind a load-balanced DNS hostname; per-instance
    config (price, capabilities, hardware) lives on `OrchestratorInstance` and
    can legitimately differ across instances even under the same eth address."""

    tenant_id: str = Field(alias="tenantId")
    orch_addr: str = Field(alias="orchAddr")
    last_seen: datetime = Field(alias="lastSeen")
    updated_at: datetime = Field(alias="updatedAt")


class OrchestratorWithDetailsDefaultInstances(BaseModel):
    """Per-instance state. One per (tenant, orch_addr, resolved_ip). Multiple
    instances of the same orch can have independent prices, capabilities, and
    hardware — the side panel renders this list so divergence is visible."""

    tenant_id: str = Field(alias="tenantId")
    orch_addr: str = Field(alias="orchAddr")
    resolved_ip: str = Field(alias="resolvedIp")
    canonical_url: str = Field(alias="canonicalUrl")
    advertised_node_urls: list[str] = Field(alias="advertisedNodeUrls")
    capabilities: list[str]
    price_per_unit_eth: str = Field(alias="pricePerUnitEth")
    pixels_per_unit: str = Field(alias="pixelsPerUnit")
    capability_prices: list[
        "OrchestratorWithDetailsDefaultInstancesCapabilityPrices"
    ] = Field(alias="capabilityPrices")
    hardware: str
    source: str
    last_seen: datetime = Field(alias="lastSeen")
    updated_at: datetime = Field(alias="updatedAt")


class OrchestratorWithDetailsDefaultInstancesCapabilityPrices(BaseModel):
    """One per-capability price entry. The gateway emits these as typed
    `OrchestratorCapabilityPriceEntry` proto messages; if `capability` is empty
    it falls back to `pos:<N>` reflecting the index in upstream's
    `capabilities_prices` array. Prices are exposed as ETH-denominated decimal
    strings so clients never handle wei-scale integers through JSON floats."""

    capability: str
    price_per_unit_eth: str = Field(alias="pricePerUnitEth")
    pixels_per_unit: str = Field(alias="pixelsPerUnit")


class OrchestratorWithDetailsDefaultVantages(BaseModel):
    """Per-vantage observation: one row per (cluster owner tenant, gateway, orch
    address, resolved IP). DNS round-robin / geo-anycast surfaces as multiple
    vantages with different `resolvedIp`."""

    tenant_id: str = Field(alias="tenantId")
    gateway_id: str = Field(alias="gatewayId")
    gateway_region: str = Field(alias="gatewayRegion")
    orch_addr: str = Field(alias="orchAddr")
    resolved_ip: str = Field(alias="resolvedIp")
    latitude: float
    longitude: float
    city: str
    country_code: str = Field(alias="countryCode")
    geo_source: str = Field(alias="geoSource")
    geo_resolved_at: Optional[datetime] = Field(
        alias="geoResolvedAt",
        description="When the vantage's location was resolved; null while it has not been.",
    )
    "When the vantage's location was resolved; null while it has not been."
    latest_latency_ms: int = Field(alias="latestLatencyMs")
    score: float
    dialed_recently: bool = Field(alias="dialedRecently")
    last_seen: datetime = Field(alias="lastSeen")


class OrchestratorsConnectionDefault(BaseModel):
    """Pagination wrapper for orchestrator listing."""

    nodes: list["OrchestratorsConnectionDefaultNodes"]
    total_count: int = Field(alias="totalCount")


class OrchestratorsConnectionDefaultNodes(BaseModel):
    """Identity-level orchestrator row — keyed by eth address. An orchestrator
    typically fronts N instances behind a load-balanced DNS hostname; per-instance
    config (price, capabilities, hardware) lives on `OrchestratorInstance` and
    can legitimately differ across instances even under the same eth address."""

    tenant_id: str = Field(alias="tenantId")
    orch_addr: str = Field(alias="orchAddr")
    last_seen: datetime = Field(alias="lastSeen")
    updated_at: datetime = Field(alias="updatedAt")


class PageInfoDefault(BaseModel):
    start_cursor: Optional[str] = Field(alias="startCursor")
    end_cursor: Optional[str] = Field(alias="endCursor")
    has_next_page: bool = Field(alias="hasNextPage")
    has_previous_page: bool = Field(alias="hasPreviousPage")


class PageInfo(BaseModel):
    start_cursor: Optional[str] = Field(alias="startCursor")
    end_cursor: Optional[str] = Field(alias="endCursor")
    has_next_page: bool = Field(alias="hasNextPage")
    has_previous_page: bool = Field(alias="hasPreviousPage")


class PaymentDefault(BaseModel):
    id: str
    payment_url: Optional[str] = Field(alias="paymentUrl")
    wallet_address: Optional[str] = Field(alias="walletAddress")
    amount: Any
    currency: Any
    method: PaymentMethod
    status: PaymentStatus
    expires_at: Optional[datetime] = Field(alias="expiresAt")
    qr_code: Optional[str] = Field(alias="qrCode")
    expected_amount_base_units: Optional[str] = Field(alias="expectedAmountBaseUnits")
    expected_amount_token: Optional[str] = Field(alias="expectedAmountToken")
    quoted_price_usd: Optional[str] = Field(alias="quotedPriceUsd")
    quote_source: Optional[str] = Field(alias="quoteSource")
    asset_symbol: Optional[str] = Field(alias="assetSymbol")
    network: Optional[str]
    quoted_at: Optional[datetime] = Field(alias="quotedAt")
    created_at: datetime = Field(alias="createdAt")
    conversion: Optional["PaymentDefaultConversion"] = Field(
        description="Amount charged and the EUR amount applied to the invoice, at the ECB rate of payment creation."
    )
    "Amount charged and the EUR amount applied to the invoice, at the ECB rate of payment creation."


class PaymentDefaultConversion(BaseModel):
    """Conversion of a payment into the EUR prepaid ledger and invoice currency.
    EUR amounts use the identity rate."""

    original_amount_cents: int = Field(
        alias="originalAmountCents",
        description="Amount charged, in cents of `originalCurrency`.",
    )
    "Amount charged, in cents of `originalCurrency`."
    original_currency: Any = Field(
        alias="originalCurrency", description="Currency charged (EUR, USD, GBP)."
    )
    "Currency charged (EUR, USD, GBP)."
    eur_amount_cents: int = Field(
        alias="eurAmountCents", description="EUR cents credited or applied."
    )
    "EUR cents credited or applied."
    units_per_eur: str = Field(
        alias="unitsPerEur",
        description='Units of `originalCurrency` one euro buys, as a decimal string ("1" for EUR).',
    )
    'Units of `originalCurrency` one euro buys, as a decimal string ("1" for EUR).'
    source: str = Field(
        description="Rate source: identity (EUR), ecb (ECB reference rate), or legacy_quote (USD rate quoted before ECB rates were stored)."
    )
    "Rate source: identity (EUR), ecb (ECB reference rate), or legacy_quote (USD rate quoted before ECB rates were stored)."
    reference_date: str = Field(
        alias="referenceDate",
        description="ECB reference date of the rate (YYYY-MM-DD).",
    )
    "ECB reference date of the rate (YYYY-MM-DD)."


class PlatformOverviewDefault(BaseModel):
    total_streams: int = Field(alias="totalStreams")
    active_streams: int = Field(alias="activeStreams")
    total_viewers: int = Field(alias="totalViewers")
    average_viewers: float = Field(alias="averageViewers")
    total_bandwidth: float = Field(alias="totalBandwidth")
    peak_bandwidth: float = Field(alias="peakBandwidth")
    stream_hours: float = Field(alias="streamHours")
    egress_gb: float = Field(alias="egressGb")
    peak_viewers: int = Field(alias="peakViewers")
    time_range: "PlatformOverviewDefaultTimeRange" = Field(alias="timeRange")
    total_upload_bytes: float = Field(alias="totalUploadBytes")
    total_download_bytes: float = Field(alias="totalDownloadBytes")
    viewer_hours: float = Field(alias="viewerHours")
    delivered_minutes: float = Field(alias="deliveredMinutes")
    unique_viewers: int = Field(alias="uniqueViewers")
    ingest_hours: float = Field(alias="ingestHours")
    peak_concurrent_viewers: int = Field(alias="peakConcurrentViewers")
    total_views: int = Field(alias="totalViews")


class PlatformOverviewDefaultTimeRange(BaseModel):
    """Time range returned in query results."""

    start: datetime = Field(description="Start of the time range.")
    "Start of the time range."
    end: datetime = Field(description="End of the time range.")
    "End of the time range."


class PlayerBootSummaryDefault(BaseModel):
    """Player startup (boot) summary from player_boot_samples. Diagnostic only — never
    a viewer-count or billing source. TTF percentiles are computed at read time over
    boots that reached first frame; counts cover all rows."""

    boot_count: int = Field(alias="bootCount")
    error_count: int = Field(alias="errorCount")
    p_50_ttf_ms: float = Field(alias="p50TtfMs")
    p_95_ttf_ms: float = Field(alias="p95TtfMs")
    p_99_ttf_ms: float = Field(alias="p99TtfMs")
    avg_gateway_resolve_ms: float = Field(alias="avgGatewayResolveMs")
    avg_mist_hydrate_ms: float = Field(alias="avgMistHydrateMs")
    avg_player_select_ms: float = Field(alias="avgPlayerSelectMs")
    avg_connect_ms: float = Field(alias="avgConnectMs")
    avg_prebuffer_ms: float = Field(alias="avgPrebufferMs")
    cache_hit_ratio: float = Field(alias="cacheHitRatio")


class PlayerBootTimeSeriesBucketDefault(BaseModel):
    """One toStartOfInterval window of the boot-startup summary. TTF percentiles are
    computed at read time per bucket; `bootCount` is the per-bucket denominator."""

    timestamp: datetime
    boot_count: int = Field(alias="bootCount")
    p_50_ttf_ms: float = Field(alias="p50TtfMs")
    p_95_ttf_ms: float = Field(alias="p95TtfMs")
    p_99_ttf_ms: float = Field(alias="p99TtfMs")


class PrepaidBalanceDefault(BaseModel):
    """Prepaid balance for wallet-based accounts.
    Wallet-only accounts MUST use prepaid billing (balance-based, pay first)."""

    id: str = Field(description="Unique balance record identifier.")
    "Unique balance record identifier."
    tenant_id: str = Field(alias="tenantId", description="Owning tenant identifier.")
    "Owning tenant identifier."
    balance_cents: int = Field(
        alias="balanceCents",
        description="Settled ledger balance in cents before active usage reservations.",
    )
    "Settled ledger balance in cents before active usage reservations."
    reserved_balance_cents: int = Field(
        alias="reservedBalanceCents",
        description="Active in-flight usage reservations in cents.",
    )
    "Active in-flight usage reservations in cents."
    available_balance_cents: int = Field(
        alias="availableBalanceCents",
        description="Spendable balance after active reservations (balanceCents - reservedBalanceCents).",
    )
    "Spendable balance after active reservations (balanceCents - reservedBalanceCents)."
    currency: str = Field(description="Ledger currency, always EUR.")
    "Ledger currency, always EUR."
    low_balance_threshold_cents: int = Field(
        alias="lowBalanceThresholdCents", description="Alert threshold in cents."
    )
    "Alert threshold in cents."
    is_low_balance: bool = Field(
        alias="isLowBalance",
        description="True if available balance is below threshold.",
    )
    "True if available balance is below threshold."
    drain_rate_cents_per_hour: int = Field(
        alias="drainRateCentsPerHour",
        description="Estimated spend rate in cents per hour (based on last hour's usage).",
    )
    "Estimated spend rate in cents per hour (based on last hour's usage)."
    created_at: datetime = Field(
        alias="createdAt", description="When the balance was created."
    )
    "When the balance was created."
    updated_at: datetime = Field(
        alias="updatedAt", description="When the balance was last updated."
    )
    "When the balance was last updated."


class ProcessingUsageRecordDefault(BaseModel):
    id: str
    timestamp: datetime
    node_id: str = Field(alias="nodeId")
    stream_id: str = Field(alias="streamId")
    stream: Optional["ProcessingUsageRecordDefaultStream"]
    process_type: str = Field(alias="processType")
    cluster_id: Optional[str] = Field(alias="clusterId")
    origin_cluster_id: Optional[str] = Field(alias="originClusterId")
    control_cell_id: Optional[str] = Field(alias="controlCellId")
    track_type: Optional[str] = Field(alias="trackType")
    duration_ms: int = Field(alias="durationMs")
    input_codec: Optional[str] = Field(alias="inputCodec")
    output_codec: Optional[str] = Field(alias="outputCodec")
    segment_number: Optional[int] = Field(alias="segmentNumber")
    width: Optional[int]
    height: Optional[int]
    rendition_count: Optional[int] = Field(alias="renditionCount")
    broadcaster_url: Optional[str] = Field(alias="broadcasterUrl")
    upload_time_us: Optional[int] = Field(alias="uploadTimeUs")
    livepeer_session_id: Optional[str] = Field(alias="livepeerSessionId")
    segment_start_ms: Optional[int] = Field(alias="segmentStartMs")
    input_bytes: Optional[float] = Field(alias="inputBytes")
    output_bytes_total: Optional[float] = Field(alias="outputBytesTotal")
    attempt_count: Optional[int] = Field(alias="attemptCount")
    turnaround_ms: Optional[int] = Field(alias="turnaroundMs")
    speed_factor: Optional[float] = Field(alias="speedFactor")
    renditions_json: Optional[str] = Field(alias="renditionsJson")
    input_frames: Optional[int] = Field(alias="inputFrames")
    output_frames: Optional[int] = Field(alias="outputFrames")
    decode_us_per_frame: Optional[int] = Field(alias="decodeUsPerFrame")
    transform_us_per_frame: Optional[int] = Field(alias="transformUsPerFrame")
    encode_us_per_frame: Optional[int] = Field(alias="encodeUsPerFrame")
    is_final: Optional[bool] = Field(alias="isFinal")
    input_frames_delta: Optional[int] = Field(alias="inputFramesDelta")
    output_frames_delta: Optional[int] = Field(alias="outputFramesDelta")
    input_bytes_delta: Optional[float] = Field(alias="inputBytesDelta")
    output_bytes_delta: Optional[float] = Field(alias="outputBytesDelta")
    input_width: Optional[int] = Field(alias="inputWidth")
    input_height: Optional[int] = Field(alias="inputHeight")
    output_width: Optional[int] = Field(alias="outputWidth")
    output_height: Optional[int] = Field(alias="outputHeight")
    input_fpks: Optional[int] = Field(alias="inputFpks")
    output_fps_measured: Optional[float] = Field(alias="outputFpsMeasured")
    sample_rate: Optional[int] = Field(alias="sampleRate")
    channels: Optional[int]
    source_timestamp_ms: Optional[int] = Field(alias="sourceTimestampMs")
    sink_timestamp_ms: Optional[int] = Field(alias="sinkTimestampMs")
    source_advanced_ms: Optional[int] = Field(alias="sourceAdvancedMs")
    sink_advanced_ms: Optional[int] = Field(alias="sinkAdvancedMs")
    rtf_in: Optional[float] = Field(alias="rtfIn")
    rtf_out: Optional[float] = Field(alias="rtfOut")
    pipeline_lag_ms: Optional[int] = Field(alias="pipelineLagMs")
    output_bitrate_bps: Optional[int] = Field(alias="outputBitrateBps")


class ProcessingUsageRecordDefaultStream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

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
        description="Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path.",
    )
    "Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path."
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
    pull_source: Optional["ProcessingUsageRecordDefaultStreamPullSource"] = Field(
        alias="pullSource",
        description="Pull-source config for pull streams; null for push streams.",
    )
    "Pull-source config for pull streams; null for push streams."
    managed_source: Optional["ProcessingUsageRecordDefaultStreamManagedSource"] = Field(
        alias="managedSource",
        description="Safe source summary for managed streams; null for push and pull streams.",
    )
    "Safe source summary for managed streams; null for push and pull streams."
    source_location: "ProcessingUsageRecordDefaultStreamSourceLocation" = Field(
        alias="sourceLocation",
        description="Where the stream's source may be ingested, derived from the stream's own ingest placement rules.",
    )
    "Where the stream's source may be ingested, derived from the stream's own ingest placement rules."
    created_at: datetime = Field(
        alias="createdAt", description="When this stream was created."
    )
    "When this stream was created."
    updated_at: datetime = Field(
        alias="updatedAt", description="When this stream was last modified."
    )
    "When this stream was last modified."
    metrics: Optional["ProcessingUsageRecordDefaultStreamMetrics"] = Field(
        description="Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    )
    "Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    push_targets: list["ProcessingUsageRecordDefaultStreamPushTargets"] = Field(
        alias="pushTargets",
        description="Configured multistream push targets for this stream.",
    )
    "Configured multistream push targets for this stream."
    playback_policy: Optional["ProcessingUsageRecordDefaultStreamPlaybackPolicy"] = (
        Field(
            alias="playbackPolicy",
            description="Playback access policy. null/PUBLIC = anyone with the playbackId can watch.",
        )
    )
    "Playback access policy. null/PUBLIC = anyone with the playbackId can watch."
    dvr_chapter_mode: Optional[DVRChapterMode] = Field(
        alias="dvrChapterMode",
        description="How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast.",
    )
    "How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast."
    dvr_chapter_interval_seconds: Optional[int] = Field(
        alias="dvrChapterIntervalSeconds",
        description="Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour).",
    )
    "Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour)."
    monitoring: MonitoringToggle = Field(
        description="Per-stream Skipper monitoring override (INHERIT follows tier)."
    )
    "Per-stream Skipper monitoring override (INHERIT follows tier)."
    thumbnail_assets: Optional["ProcessingUsageRecordDefaultStreamThumbnailAssets"] = (
        Field(
            alias="thumbnailAssets",
            description="Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles.",
        )
    )
    "Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles."
    retention_overrides: Optional[
        "ProcessingUsageRecordDefaultStreamRetentionOverrides"
    ] = Field(
        alias="retentionOverrides",
        description="Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here.",
    )
    "Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here."


class ProcessingUsageRecordDefaultStreamPullSource(BaseModel):
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


class ProcessingUsageRecordDefaultStreamManagedSource(BaseModel):
    """Safe summary of an operator-managed source. Literal paths, commands, and
    credentials are intentionally not exposed through the tenant API."""

    source_kind: str = Field(
        alias="sourceKind", description="Managed source form: file, playlist, or exec."
    )
    "Managed source form: file, playlist, or exec."
    always_on: bool = Field(
        alias="alwaysOn",
        description="Whether the source is kept active without waiting for a viewer.",
    )
    "Whether the source is kept active without waiting for a viewer."
    placement_count: int = Field(
        alias="placementCount",
        description="Number of source placements requested by the operator configuration.",
    )
    "Number of source placements requested by the operator configuration."


class ProcessingUsageRecordDefaultStreamSourceLocation(BaseModel):
    mode: SourceLocationMode
    avoid_node_ids: list[str] = Field(
        alias="avoidNodeIds", description="Empty unless mode is RESTRICTED."
    )
    "Empty unless mode is RESTRICTED."


class ProcessingUsageRecordDefaultStreamMetrics(BaseModel):
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
    node_id: Optional[str] = Field(
        alias="nodeId", description="ID of the edge node handling this stream."
    )
    "ID of the edge node handling this stream."
    track_count: Optional[int] = Field(
        alias="trackCount", description="Number of quality tracks available."
    )
    "Number of quality tracks available."
    total_inputs: Optional[int] = Field(
        alias="totalInputs", description="Total ingest connections to this stream."
    )
    "Total ingest connections to this stream."
    uploaded_bytes: float = Field(
        alias="uploadedBytes",
        description="Total bytes uploaded (ingested) for current session.",
    )
    "Total bytes uploaded (ingested) for current session."
    downloaded_bytes: float = Field(
        alias="downloadedBytes",
        description="Total bytes downloaded (egress) for current session.",
    )
    "Total bytes downloaded (egress) for current session."
    viewer_seconds: float = Field(
        alias="viewerSeconds", description="Total viewer-seconds accumulated."
    )
    "Total viewer-seconds accumulated."
    packets_sent: Optional[float] = Field(
        alias="packetsSent", description="Total packets sent to viewers."
    )
    "Total packets sent to viewers."
    packets_lost: Optional[float] = Field(
        alias="packetsLost", description="Total packets lost in transit."
    )
    "Total packets lost in transit."
    packets_retransmitted: Optional[float] = Field(
        alias="packetsRetransmitted", description="Total packets retransmitted."
    )
    "Total packets retransmitted."
    buffer_state: Optional[str] = Field(
        alias="bufferState",
        description="Buffer health state (HEALTHY, WARNING, CRITICAL).",
    )
    "Buffer health state (HEALTHY, WARNING, CRITICAL)."
    quality_tier: Optional[str] = Field(
        alias="qualityTier",
        description="Highest quality tier available (4K, 1080p, 720p, etc.).",
    )
    "Highest quality tier available (4K, 1080p, 720p, etc.)."
    primary_width: Optional[int] = Field(
        alias="primaryWidth", description="Primary video track width in pixels."
    )
    "Primary video track width in pixels."
    primary_height: Optional[int] = Field(
        alias="primaryHeight", description="Primary video track height in pixels."
    )
    "Primary video track height in pixels."
    primary_fps: Optional[float] = Field(
        alias="primaryFps", description="Primary video track framerate."
    )
    "Primary video track framerate."
    primary_codec: Optional[str] = Field(
        alias="primaryCodec",
        description="Primary video codec (H.264, H.265, VP9, AV1).",
    )
    "Primary video codec (H.264, H.265, VP9, AV1)."
    primary_bitrate: Optional[int] = Field(
        alias="primaryBitrate", description="Primary video bitrate in kbps."
    )
    "Primary video bitrate in kbps."
    has_issues: Optional[bool] = Field(
        alias="hasIssues", description="Whether the stream has active quality issues."
    )
    "Whether the stream has active quality issues."
    issues_description: Optional[str] = Field(
        alias="issuesDescription",
        description="Human-readable description of current issues.",
    )
    "Human-readable description of current issues."


class ProcessingUsageRecordDefaultStreamPushTargets(BaseModel):
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


class ProcessingUsageRecordDefaultStreamPlaybackPolicy(BaseModel):
    """Per-playback-object access policy. Foghorn reads this in the USER_NEW
    trigger handler; the requiresAuth marker (on the playback object itself)
    gates whether the full policy is fetched at all."""

    type_: PlaybackPolicyType = Field(alias="type")
    allowed_origins: list[str] = Field(
        alias="allowedOrigins",
        description="Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied.",
    )
    "Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied."


class ProcessingUsageRecordDefaultStreamThumbnailAssets(BaseModel):
    """Chandler-served thumbnail asset URLs (poster, sprite, VTT cues). URL shape
    is `{chandlerBase}/assets/{assetKey}/poster.jpg`,
    `/assets/{assetKey}/sprite.jpg`, `/assets/{assetKey}/sprite.vtt` — Chandler
    serves the object key directly, no version resolution. assetKey is stream_id
    for live streams; clip_hash / dvr_hash / vod_hash (= artifact_hash) for artifacts."""

    poster_url: str = Field(alias="posterUrl")
    sprite_vtt_url: str = Field(alias="spriteVttUrl")
    sprite_jpg_url: str = Field(alias="spriteJpgUrl")
    asset_key: str = Field(alias="assetKey")


class ProcessingUsageRecordDefaultStreamRetentionOverrides(BaseModel):
    """Per-stream retention overrides for DVR and clips. VOD uploads aren't
    stream-bound, so they have no stream-level knob here."""

    stream_id: str = Field(alias="streamId")
    dvr_retention_days_override: Optional[int] = Field(
        alias="dvrRetentionDaysOverride",
        description="Null = no override (inherit tenant default). 0 = no auto-expire.",
    )
    "Null = no override (inherit tenant default). 0 = no auto-expire."
    clip_retention_days_override: Optional[int] = Field(
        alias="clipRetentionDaysOverride"
    )


class ProcessingUsageRecordInNodeDefault(BaseModel):
    id: str
    timestamp: datetime
    node_id: str = Field(alias="nodeId")
    stream_id: str = Field(alias="streamId")
    stream: Optional["ProcessingUsageRecordInNodeDefaultStream"]
    process_type: str = Field(alias="processType")
    cluster_id: Optional[str] = Field(alias="clusterId")
    origin_cluster_id: Optional[str] = Field(alias="originClusterId")
    control_cell_id: Optional[str] = Field(alias="controlCellId")
    track_type: Optional[str] = Field(alias="trackType")
    processing_usage_record_duration_ms: int = Field(
        alias="processingUsageRecordDurationMs"
    )
    input_codec: Optional[str] = Field(alias="inputCodec")
    output_codec: Optional[str] = Field(alias="outputCodec")
    segment_number: Optional[int] = Field(alias="segmentNumber")
    width: Optional[int]
    height: Optional[int]
    rendition_count: Optional[int] = Field(alias="renditionCount")
    broadcaster_url: Optional[str] = Field(alias="broadcasterUrl")
    upload_time_us: Optional[int] = Field(alias="uploadTimeUs")
    livepeer_session_id: Optional[str] = Field(alias="livepeerSessionId")
    segment_start_ms: Optional[int] = Field(alias="segmentStartMs")
    input_bytes: Optional[float] = Field(alias="inputBytes")
    output_bytes_total: Optional[float] = Field(alias="outputBytesTotal")
    attempt_count: Optional[int] = Field(alias="attemptCount")
    turnaround_ms: Optional[int] = Field(alias="turnaroundMs")
    speed_factor: Optional[float] = Field(alias="speedFactor")
    renditions_json: Optional[str] = Field(alias="renditionsJson")
    input_frames: Optional[int] = Field(alias="inputFrames")
    output_frames: Optional[int] = Field(alias="outputFrames")
    decode_us_per_frame: Optional[int] = Field(alias="decodeUsPerFrame")
    transform_us_per_frame: Optional[int] = Field(alias="transformUsPerFrame")
    encode_us_per_frame: Optional[int] = Field(alias="encodeUsPerFrame")
    is_final: Optional[bool] = Field(alias="isFinal")
    input_frames_delta: Optional[int] = Field(alias="inputFramesDelta")
    output_frames_delta: Optional[int] = Field(alias="outputFramesDelta")
    input_bytes_delta: Optional[float] = Field(alias="inputBytesDelta")
    output_bytes_delta: Optional[float] = Field(alias="outputBytesDelta")
    input_width: Optional[int] = Field(alias="inputWidth")
    input_height: Optional[int] = Field(alias="inputHeight")
    output_width: Optional[int] = Field(alias="outputWidth")
    output_height: Optional[int] = Field(alias="outputHeight")
    input_fpks: Optional[int] = Field(alias="inputFpks")
    output_fps_measured: Optional[float] = Field(alias="outputFpsMeasured")
    sample_rate: Optional[int] = Field(alias="sampleRate")
    channels: Optional[int]
    source_timestamp_ms: Optional[int] = Field(alias="sourceTimestampMs")
    sink_timestamp_ms: Optional[int] = Field(alias="sinkTimestampMs")
    source_advanced_ms: Optional[int] = Field(alias="sourceAdvancedMs")
    sink_advanced_ms: Optional[int] = Field(alias="sinkAdvancedMs")
    rtf_in: Optional[float] = Field(alias="rtfIn")
    rtf_out: Optional[float] = Field(alias="rtfOut")
    pipeline_lag_ms: Optional[int] = Field(alias="pipelineLagMs")
    output_bitrate_bps: Optional[int] = Field(alias="outputBitrateBps")


class ProcessingUsageRecordInNodeDefaultStream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

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
        description="Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path.",
    )
    "Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path."
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
    pull_source: Optional["ProcessingUsageRecordInNodeDefaultStreamPullSource"] = Field(
        alias="pullSource",
        description="Pull-source config for pull streams; null for push streams.",
    )
    "Pull-source config for pull streams; null for push streams."
    managed_source: Optional[
        "ProcessingUsageRecordInNodeDefaultStreamManagedSource"
    ] = Field(
        alias="managedSource",
        description="Safe source summary for managed streams; null for push and pull streams.",
    )
    "Safe source summary for managed streams; null for push and pull streams."
    source_location: "ProcessingUsageRecordInNodeDefaultStreamSourceLocation" = Field(
        alias="sourceLocation",
        description="Where the stream's source may be ingested, derived from the stream's own ingest placement rules.",
    )
    "Where the stream's source may be ingested, derived from the stream's own ingest placement rules."
    created_at: datetime = Field(
        alias="createdAt", description="When this stream was created."
    )
    "When this stream was created."
    updated_at: datetime = Field(
        alias="updatedAt", description="When this stream was last modified."
    )
    "When this stream was last modified."
    metrics: Optional["ProcessingUsageRecordInNodeDefaultStreamMetrics"] = Field(
        description="Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    )
    "Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    push_targets: list["ProcessingUsageRecordInNodeDefaultStreamPushTargets"] = Field(
        alias="pushTargets",
        description="Configured multistream push targets for this stream.",
    )
    "Configured multistream push targets for this stream."
    playback_policy: Optional[
        "ProcessingUsageRecordInNodeDefaultStreamPlaybackPolicy"
    ] = Field(
        alias="playbackPolicy",
        description="Playback access policy. null/PUBLIC = anyone with the playbackId can watch.",
    )
    "Playback access policy. null/PUBLIC = anyone with the playbackId can watch."
    dvr_chapter_mode: Optional[DVRChapterMode] = Field(
        alias="dvrChapterMode",
        description="How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast.",
    )
    "How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast."
    dvr_chapter_interval_seconds: Optional[int] = Field(
        alias="dvrChapterIntervalSeconds",
        description="Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour).",
    )
    "Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour)."
    monitoring: MonitoringToggle = Field(
        description="Per-stream Skipper monitoring override (INHERIT follows tier)."
    )
    "Per-stream Skipper monitoring override (INHERIT follows tier)."
    thumbnail_assets: Optional[
        "ProcessingUsageRecordInNodeDefaultStreamThumbnailAssets"
    ] = Field(
        alias="thumbnailAssets",
        description="Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles.",
    )
    "Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles."
    retention_overrides: Optional[
        "ProcessingUsageRecordInNodeDefaultStreamRetentionOverrides"
    ] = Field(
        alias="retentionOverrides",
        description="Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here.",
    )
    "Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here."


class ProcessingUsageRecordInNodeDefaultStreamPullSource(BaseModel):
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


class ProcessingUsageRecordInNodeDefaultStreamManagedSource(BaseModel):
    """Safe summary of an operator-managed source. Literal paths, commands, and
    credentials are intentionally not exposed through the tenant API."""

    source_kind: str = Field(
        alias="sourceKind", description="Managed source form: file, playlist, or exec."
    )
    "Managed source form: file, playlist, or exec."
    always_on: bool = Field(
        alias="alwaysOn",
        description="Whether the source is kept active without waiting for a viewer.",
    )
    "Whether the source is kept active without waiting for a viewer."
    placement_count: int = Field(
        alias="placementCount",
        description="Number of source placements requested by the operator configuration.",
    )
    "Number of source placements requested by the operator configuration."


class ProcessingUsageRecordInNodeDefaultStreamSourceLocation(BaseModel):
    mode: SourceLocationMode
    avoid_node_ids: list[str] = Field(
        alias="avoidNodeIds", description="Empty unless mode is RESTRICTED."
    )
    "Empty unless mode is RESTRICTED."


class ProcessingUsageRecordInNodeDefaultStreamMetrics(BaseModel):
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
    node_id: Optional[str] = Field(
        alias="nodeId", description="ID of the edge node handling this stream."
    )
    "ID of the edge node handling this stream."
    track_count: Optional[int] = Field(
        alias="trackCount", description="Number of quality tracks available."
    )
    "Number of quality tracks available."
    total_inputs: Optional[int] = Field(
        alias="totalInputs", description="Total ingest connections to this stream."
    )
    "Total ingest connections to this stream."
    uploaded_bytes: float = Field(
        alias="uploadedBytes",
        description="Total bytes uploaded (ingested) for current session.",
    )
    "Total bytes uploaded (ingested) for current session."
    downloaded_bytes: float = Field(
        alias="downloadedBytes",
        description="Total bytes downloaded (egress) for current session.",
    )
    "Total bytes downloaded (egress) for current session."
    viewer_seconds: float = Field(
        alias="viewerSeconds", description="Total viewer-seconds accumulated."
    )
    "Total viewer-seconds accumulated."
    packets_sent: Optional[float] = Field(
        alias="packetsSent", description="Total packets sent to viewers."
    )
    "Total packets sent to viewers."
    packets_lost: Optional[float] = Field(
        alias="packetsLost", description="Total packets lost in transit."
    )
    "Total packets lost in transit."
    packets_retransmitted: Optional[float] = Field(
        alias="packetsRetransmitted", description="Total packets retransmitted."
    )
    "Total packets retransmitted."
    buffer_state: Optional[str] = Field(
        alias="bufferState",
        description="Buffer health state (HEALTHY, WARNING, CRITICAL).",
    )
    "Buffer health state (HEALTHY, WARNING, CRITICAL)."
    quality_tier: Optional[str] = Field(
        alias="qualityTier",
        description="Highest quality tier available (4K, 1080p, 720p, etc.).",
    )
    "Highest quality tier available (4K, 1080p, 720p, etc.)."
    primary_width: Optional[int] = Field(
        alias="primaryWidth", description="Primary video track width in pixels."
    )
    "Primary video track width in pixels."
    primary_height: Optional[int] = Field(
        alias="primaryHeight", description="Primary video track height in pixels."
    )
    "Primary video track height in pixels."
    primary_fps: Optional[float] = Field(
        alias="primaryFps", description="Primary video track framerate."
    )
    "Primary video track framerate."
    primary_codec: Optional[str] = Field(
        alias="primaryCodec",
        description="Primary video codec (H.264, H.265, VP9, AV1).",
    )
    "Primary video codec (H.264, H.265, VP9, AV1)."
    primary_bitrate: Optional[int] = Field(
        alias="primaryBitrate", description="Primary video bitrate in kbps."
    )
    "Primary video bitrate in kbps."
    has_issues: Optional[bool] = Field(
        alias="hasIssues", description="Whether the stream has active quality issues."
    )
    "Whether the stream has active quality issues."
    issues_description: Optional[str] = Field(
        alias="issuesDescription",
        description="Human-readable description of current issues.",
    )
    "Human-readable description of current issues."


class ProcessingUsageRecordInNodeDefaultStreamPushTargets(BaseModel):
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


class ProcessingUsageRecordInNodeDefaultStreamPlaybackPolicy(BaseModel):
    """Per-playback-object access policy. Foghorn reads this in the USER_NEW
    trigger handler; the requiresAuth marker (on the playback object itself)
    gates whether the full policy is fetched at all."""

    type_: PlaybackPolicyType = Field(alias="type")
    allowed_origins: list[str] = Field(
        alias="allowedOrigins",
        description="Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied.",
    )
    "Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied."


class ProcessingUsageRecordInNodeDefaultStreamThumbnailAssets(BaseModel):
    """Chandler-served thumbnail asset URLs (poster, sprite, VTT cues). URL shape
    is `{chandlerBase}/assets/{assetKey}/poster.jpg`,
    `/assets/{assetKey}/sprite.jpg`, `/assets/{assetKey}/sprite.vtt` — Chandler
    serves the object key directly, no version resolution. assetKey is stream_id
    for live streams; clip_hash / dvr_hash / vod_hash (= artifact_hash) for artifacts."""

    poster_url: str = Field(alias="posterUrl")
    sprite_vtt_url: str = Field(alias="spriteVttUrl")
    sprite_jpg_url: str = Field(alias="spriteJpgUrl")
    asset_key: str = Field(alias="assetKey")


class ProcessingUsageRecordInNodeDefaultStreamRetentionOverrides(BaseModel):
    """Per-stream retention overrides for DVR and clips. VOD uploads aren't
    stream-bound, so they have no stream-level knob here."""

    stream_id: str = Field(alias="streamId")
    dvr_retention_days_override: Optional[int] = Field(
        alias="dvrRetentionDaysOverride",
        description="Null = no override (inherit tenant default). 0 = no auto-expire.",
    )
    "Null = no override (inherit tenant default). 0 = no auto-expire."
    clip_retention_days_override: Optional[int] = Field(
        alias="clipRetentionDaysOverride"
    )


class PromoteToPaidPayloadDefault(BaseModel):
    """Successful promotion to postpaid billing."""

    success: bool = Field(description="Whether the promotion succeeded.")
    "Whether the promotion succeeded."
    message: str = Field(description="Human-readable status message.")
    "Human-readable status message."
    new_billing_model: str = Field(
        alias="newBillingModel", description="The new billing model (postpaid)."
    )
    "The new billing model (postpaid)."
    credit_balance_cents: int = Field(
        alias="creditBalanceCents",
        description="Prepaid balance carried forward as credit (in cents).",
    )
    "Prepaid balance carried forward as credit (in cents)."
    subscription_id: str = Field(
        alias="subscriptionId", description="The new subscription ID."
    )
    "The new subscription ID."


class PullSourceEventDefault(BaseModel):
    """A single pull-source resolution outcome from Foghorn's STREAM_SOURCE
    handler. Append-only; written every time a pull+ stream's source is
    re-evaluated (typically on each Mist input start)."""

    id: str
    internal_name: str = Field(alias="internalName")
    event_kind: str = Field(
        alias="eventKind",
        description="'resolved' | 'not_found' | 'disabled' | 'blocked_uri' | 'cluster_not_allowed_delegate' | 'commodore_error' | 'foghorn_base_unresolved'",
    )
    "'resolved' | 'not_found' | 'disabled' | 'blocked_uri' | 'cluster_not_allowed_delegate' | 'commodore_error' | 'foghorn_base_unresolved'"
    detail: Optional[str]
    created_at: datetime = Field(alias="createdAt")


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


class QualityTierDailyDefault(BaseModel):
    id: str
    day: datetime
    stream_id: str = Field(alias="streamId")
    stream: Optional["QualityTierDailyDefaultStream"]
    tier_2160_p_minutes: int = Field(alias="tier2160pMinutes")
    tier_1440_p_minutes: int = Field(alias="tier1440pMinutes")
    tier_1080_p_minutes: int = Field(alias="tier1080pMinutes")
    tier_720_p_minutes: int = Field(alias="tier720pMinutes")
    tier_480_p_minutes: int = Field(alias="tier480pMinutes")
    tier_sd_minutes: int = Field(alias="tierSdMinutes")
    primary_tier: str = Field(alias="primaryTier")
    codec_h_264_minutes: int = Field(alias="codecH264Minutes")
    codec_h_265_minutes: int = Field(alias="codecH265Minutes")
    codec_vp_9_minutes: int = Field(alias="codecVp9Minutes")
    codec_av_1_minutes: int = Field(alias="codecAv1Minutes")
    avg_bitrate: int = Field(alias="avgBitrate")
    avg_fps: float = Field(alias="avgFps")


class QualityTierDailyDefaultStream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

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
        description="Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path.",
    )
    "Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path."
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
    pull_source: Optional["QualityTierDailyDefaultStreamPullSource"] = Field(
        alias="pullSource",
        description="Pull-source config for pull streams; null for push streams.",
    )
    "Pull-source config for pull streams; null for push streams."
    managed_source: Optional["QualityTierDailyDefaultStreamManagedSource"] = Field(
        alias="managedSource",
        description="Safe source summary for managed streams; null for push and pull streams.",
    )
    "Safe source summary for managed streams; null for push and pull streams."
    source_location: "QualityTierDailyDefaultStreamSourceLocation" = Field(
        alias="sourceLocation",
        description="Where the stream's source may be ingested, derived from the stream's own ingest placement rules.",
    )
    "Where the stream's source may be ingested, derived from the stream's own ingest placement rules."
    created_at: datetime = Field(
        alias="createdAt", description="When this stream was created."
    )
    "When this stream was created."
    updated_at: datetime = Field(
        alias="updatedAt", description="When this stream was last modified."
    )
    "When this stream was last modified."
    metrics: Optional["QualityTierDailyDefaultStreamMetrics"] = Field(
        description="Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    )
    "Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    push_targets: list["QualityTierDailyDefaultStreamPushTargets"] = Field(
        alias="pushTargets",
        description="Configured multistream push targets for this stream.",
    )
    "Configured multistream push targets for this stream."
    playback_policy: Optional["QualityTierDailyDefaultStreamPlaybackPolicy"] = Field(
        alias="playbackPolicy",
        description="Playback access policy. null/PUBLIC = anyone with the playbackId can watch.",
    )
    "Playback access policy. null/PUBLIC = anyone with the playbackId can watch."
    dvr_chapter_mode: Optional[DVRChapterMode] = Field(
        alias="dvrChapterMode",
        description="How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast.",
    )
    "How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast."
    dvr_chapter_interval_seconds: Optional[int] = Field(
        alias="dvrChapterIntervalSeconds",
        description="Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour).",
    )
    "Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour)."
    monitoring: MonitoringToggle = Field(
        description="Per-stream Skipper monitoring override (INHERIT follows tier)."
    )
    "Per-stream Skipper monitoring override (INHERIT follows tier)."
    thumbnail_assets: Optional["QualityTierDailyDefaultStreamThumbnailAssets"] = Field(
        alias="thumbnailAssets",
        description="Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles.",
    )
    "Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles."
    retention_overrides: Optional["QualityTierDailyDefaultStreamRetentionOverrides"] = (
        Field(
            alias="retentionOverrides",
            description="Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here.",
        )
    )
    "Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here."


class QualityTierDailyDefaultStreamPullSource(BaseModel):
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


class QualityTierDailyDefaultStreamManagedSource(BaseModel):
    """Safe summary of an operator-managed source. Literal paths, commands, and
    credentials are intentionally not exposed through the tenant API."""

    source_kind: str = Field(
        alias="sourceKind", description="Managed source form: file, playlist, or exec."
    )
    "Managed source form: file, playlist, or exec."
    always_on: bool = Field(
        alias="alwaysOn",
        description="Whether the source is kept active without waiting for a viewer.",
    )
    "Whether the source is kept active without waiting for a viewer."
    placement_count: int = Field(
        alias="placementCount",
        description="Number of source placements requested by the operator configuration.",
    )
    "Number of source placements requested by the operator configuration."


class QualityTierDailyDefaultStreamSourceLocation(BaseModel):
    mode: SourceLocationMode
    avoid_node_ids: list[str] = Field(
        alias="avoidNodeIds", description="Empty unless mode is RESTRICTED."
    )
    "Empty unless mode is RESTRICTED."


class QualityTierDailyDefaultStreamMetrics(BaseModel):
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
    node_id: Optional[str] = Field(
        alias="nodeId", description="ID of the edge node handling this stream."
    )
    "ID of the edge node handling this stream."
    track_count: Optional[int] = Field(
        alias="trackCount", description="Number of quality tracks available."
    )
    "Number of quality tracks available."
    total_inputs: Optional[int] = Field(
        alias="totalInputs", description="Total ingest connections to this stream."
    )
    "Total ingest connections to this stream."
    uploaded_bytes: float = Field(
        alias="uploadedBytes",
        description="Total bytes uploaded (ingested) for current session.",
    )
    "Total bytes uploaded (ingested) for current session."
    downloaded_bytes: float = Field(
        alias="downloadedBytes",
        description="Total bytes downloaded (egress) for current session.",
    )
    "Total bytes downloaded (egress) for current session."
    viewer_seconds: float = Field(
        alias="viewerSeconds", description="Total viewer-seconds accumulated."
    )
    "Total viewer-seconds accumulated."
    packets_sent: Optional[float] = Field(
        alias="packetsSent", description="Total packets sent to viewers."
    )
    "Total packets sent to viewers."
    packets_lost: Optional[float] = Field(
        alias="packetsLost", description="Total packets lost in transit."
    )
    "Total packets lost in transit."
    packets_retransmitted: Optional[float] = Field(
        alias="packetsRetransmitted", description="Total packets retransmitted."
    )
    "Total packets retransmitted."
    buffer_state: Optional[str] = Field(
        alias="bufferState",
        description="Buffer health state (HEALTHY, WARNING, CRITICAL).",
    )
    "Buffer health state (HEALTHY, WARNING, CRITICAL)."
    quality_tier: Optional[str] = Field(
        alias="qualityTier",
        description="Highest quality tier available (4K, 1080p, 720p, etc.).",
    )
    "Highest quality tier available (4K, 1080p, 720p, etc.)."
    primary_width: Optional[int] = Field(
        alias="primaryWidth", description="Primary video track width in pixels."
    )
    "Primary video track width in pixels."
    primary_height: Optional[int] = Field(
        alias="primaryHeight", description="Primary video track height in pixels."
    )
    "Primary video track height in pixels."
    primary_fps: Optional[float] = Field(
        alias="primaryFps", description="Primary video track framerate."
    )
    "Primary video track framerate."
    primary_codec: Optional[str] = Field(
        alias="primaryCodec",
        description="Primary video codec (H.264, H.265, VP9, AV1).",
    )
    "Primary video codec (H.264, H.265, VP9, AV1)."
    primary_bitrate: Optional[int] = Field(
        alias="primaryBitrate", description="Primary video bitrate in kbps."
    )
    "Primary video bitrate in kbps."
    has_issues: Optional[bool] = Field(
        alias="hasIssues", description="Whether the stream has active quality issues."
    )
    "Whether the stream has active quality issues."
    issues_description: Optional[str] = Field(
        alias="issuesDescription",
        description="Human-readable description of current issues.",
    )
    "Human-readable description of current issues."


class QualityTierDailyDefaultStreamPushTargets(BaseModel):
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


class QualityTierDailyDefaultStreamPlaybackPolicy(BaseModel):
    """Per-playback-object access policy. Foghorn reads this in the USER_NEW
    trigger handler; the requiresAuth marker (on the playback object itself)
    gates whether the full policy is fetched at all."""

    type_: PlaybackPolicyType = Field(alias="type")
    allowed_origins: list[str] = Field(
        alias="allowedOrigins",
        description="Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied.",
    )
    "Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied."


class QualityTierDailyDefaultStreamThumbnailAssets(BaseModel):
    """Chandler-served thumbnail asset URLs (poster, sprite, VTT cues). URL shape
    is `{chandlerBase}/assets/{assetKey}/poster.jpg`,
    `/assets/{assetKey}/sprite.jpg`, `/assets/{assetKey}/sprite.vtt` — Chandler
    serves the object key directly, no version resolution. assetKey is stream_id
    for live streams; clip_hash / dvr_hash / vod_hash (= artifact_hash) for artifacts."""

    poster_url: str = Field(alias="posterUrl")
    sprite_vtt_url: str = Field(alias="spriteVttUrl")
    sprite_jpg_url: str = Field(alias="spriteJpgUrl")
    asset_key: str = Field(alias="assetKey")


class QualityTierDailyDefaultStreamRetentionOverrides(BaseModel):
    """Per-stream retention overrides for DVR and clips. VOD uploads aren't
    stream-bound, so they have no stream-level knob here."""

    stream_id: str = Field(alias="streamId")
    dvr_retention_days_override: Optional[int] = Field(
        alias="dvrRetentionDaysOverride",
        description="Null = no override (inherit tenant default). 0 = no auto-expire.",
    )
    "Null = no override (inherit tenant default). 0 = no auto-expire."
    clip_retention_days_override: Optional[int] = Field(
        alias="clipRetentionDaysOverride"
    )


class RateLimitErrorDefault(BaseModel):
    message: str
    code: Optional[str]
    retry_after: Optional[int] = Field(alias="retryAfter")


class RateLimitError(BaseModel):
    typename__: str = Field(alias="__typename")
    message: str
    code: Optional[str]
    retry_after: Optional[int] = Field(alias="retryAfter")


class RebufferingEventDefault(BaseModel):
    timestamp: datetime
    stream_id: str = Field(alias="streamId")
    stream: Optional["RebufferingEventDefaultStream"]
    node_id: str = Field(alias="nodeId")
    buffer_state: BufferState = Field(alias="bufferState")
    previous_state: BufferState = Field(alias="previousState")
    rebuffer_start: bool = Field(alias="rebufferStart")
    rebuffer_end: bool = Field(alias="rebufferEnd")


class RebufferingEventDefaultStream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

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
        description="Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path.",
    )
    "Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path."
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
    pull_source: Optional["RebufferingEventDefaultStreamPullSource"] = Field(
        alias="pullSource",
        description="Pull-source config for pull streams; null for push streams.",
    )
    "Pull-source config for pull streams; null for push streams."
    managed_source: Optional["RebufferingEventDefaultStreamManagedSource"] = Field(
        alias="managedSource",
        description="Safe source summary for managed streams; null for push and pull streams.",
    )
    "Safe source summary for managed streams; null for push and pull streams."
    source_location: "RebufferingEventDefaultStreamSourceLocation" = Field(
        alias="sourceLocation",
        description="Where the stream's source may be ingested, derived from the stream's own ingest placement rules.",
    )
    "Where the stream's source may be ingested, derived from the stream's own ingest placement rules."
    created_at: datetime = Field(
        alias="createdAt", description="When this stream was created."
    )
    "When this stream was created."
    updated_at: datetime = Field(
        alias="updatedAt", description="When this stream was last modified."
    )
    "When this stream was last modified."
    metrics: Optional["RebufferingEventDefaultStreamMetrics"] = Field(
        description="Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    )
    "Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    push_targets: list["RebufferingEventDefaultStreamPushTargets"] = Field(
        alias="pushTargets",
        description="Configured multistream push targets for this stream.",
    )
    "Configured multistream push targets for this stream."
    playback_policy: Optional["RebufferingEventDefaultStreamPlaybackPolicy"] = Field(
        alias="playbackPolicy",
        description="Playback access policy. null/PUBLIC = anyone with the playbackId can watch.",
    )
    "Playback access policy. null/PUBLIC = anyone with the playbackId can watch."
    dvr_chapter_mode: Optional[DVRChapterMode] = Field(
        alias="dvrChapterMode",
        description="How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast.",
    )
    "How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast."
    dvr_chapter_interval_seconds: Optional[int] = Field(
        alias="dvrChapterIntervalSeconds",
        description="Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour).",
    )
    "Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour)."
    monitoring: MonitoringToggle = Field(
        description="Per-stream Skipper monitoring override (INHERIT follows tier)."
    )
    "Per-stream Skipper monitoring override (INHERIT follows tier)."
    thumbnail_assets: Optional["RebufferingEventDefaultStreamThumbnailAssets"] = Field(
        alias="thumbnailAssets",
        description="Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles.",
    )
    "Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles."
    retention_overrides: Optional["RebufferingEventDefaultStreamRetentionOverrides"] = (
        Field(
            alias="retentionOverrides",
            description="Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here.",
        )
    )
    "Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here."


class RebufferingEventDefaultStreamPullSource(BaseModel):
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


class RebufferingEventDefaultStreamManagedSource(BaseModel):
    """Safe summary of an operator-managed source. Literal paths, commands, and
    credentials are intentionally not exposed through the tenant API."""

    source_kind: str = Field(
        alias="sourceKind", description="Managed source form: file, playlist, or exec."
    )
    "Managed source form: file, playlist, or exec."
    always_on: bool = Field(
        alias="alwaysOn",
        description="Whether the source is kept active without waiting for a viewer.",
    )
    "Whether the source is kept active without waiting for a viewer."
    placement_count: int = Field(
        alias="placementCount",
        description="Number of source placements requested by the operator configuration.",
    )
    "Number of source placements requested by the operator configuration."


class RebufferingEventDefaultStreamSourceLocation(BaseModel):
    mode: SourceLocationMode
    avoid_node_ids: list[str] = Field(
        alias="avoidNodeIds", description="Empty unless mode is RESTRICTED."
    )
    "Empty unless mode is RESTRICTED."


class RebufferingEventDefaultStreamMetrics(BaseModel):
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
    node_id: Optional[str] = Field(
        alias="nodeId", description="ID of the edge node handling this stream."
    )
    "ID of the edge node handling this stream."
    track_count: Optional[int] = Field(
        alias="trackCount", description="Number of quality tracks available."
    )
    "Number of quality tracks available."
    total_inputs: Optional[int] = Field(
        alias="totalInputs", description="Total ingest connections to this stream."
    )
    "Total ingest connections to this stream."
    uploaded_bytes: float = Field(
        alias="uploadedBytes",
        description="Total bytes uploaded (ingested) for current session.",
    )
    "Total bytes uploaded (ingested) for current session."
    downloaded_bytes: float = Field(
        alias="downloadedBytes",
        description="Total bytes downloaded (egress) for current session.",
    )
    "Total bytes downloaded (egress) for current session."
    viewer_seconds: float = Field(
        alias="viewerSeconds", description="Total viewer-seconds accumulated."
    )
    "Total viewer-seconds accumulated."
    packets_sent: Optional[float] = Field(
        alias="packetsSent", description="Total packets sent to viewers."
    )
    "Total packets sent to viewers."
    packets_lost: Optional[float] = Field(
        alias="packetsLost", description="Total packets lost in transit."
    )
    "Total packets lost in transit."
    packets_retransmitted: Optional[float] = Field(
        alias="packetsRetransmitted", description="Total packets retransmitted."
    )
    "Total packets retransmitted."
    buffer_state: Optional[str] = Field(
        alias="bufferState",
        description="Buffer health state (HEALTHY, WARNING, CRITICAL).",
    )
    "Buffer health state (HEALTHY, WARNING, CRITICAL)."
    quality_tier: Optional[str] = Field(
        alias="qualityTier",
        description="Highest quality tier available (4K, 1080p, 720p, etc.).",
    )
    "Highest quality tier available (4K, 1080p, 720p, etc.)."
    primary_width: Optional[int] = Field(
        alias="primaryWidth", description="Primary video track width in pixels."
    )
    "Primary video track width in pixels."
    primary_height: Optional[int] = Field(
        alias="primaryHeight", description="Primary video track height in pixels."
    )
    "Primary video track height in pixels."
    primary_fps: Optional[float] = Field(
        alias="primaryFps", description="Primary video track framerate."
    )
    "Primary video track framerate."
    primary_codec: Optional[str] = Field(
        alias="primaryCodec",
        description="Primary video codec (H.264, H.265, VP9, AV1).",
    )
    "Primary video codec (H.264, H.265, VP9, AV1)."
    primary_bitrate: Optional[int] = Field(
        alias="primaryBitrate", description="Primary video bitrate in kbps."
    )
    "Primary video bitrate in kbps."
    has_issues: Optional[bool] = Field(
        alias="hasIssues", description="Whether the stream has active quality issues."
    )
    "Whether the stream has active quality issues."
    issues_description: Optional[str] = Field(
        alias="issuesDescription",
        description="Human-readable description of current issues.",
    )
    "Human-readable description of current issues."


class RebufferingEventDefaultStreamPushTargets(BaseModel):
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


class RebufferingEventDefaultStreamPlaybackPolicy(BaseModel):
    """Per-playback-object access policy. Foghorn reads this in the USER_NEW
    trigger handler; the requiresAuth marker (on the playback object itself)
    gates whether the full policy is fetched at all."""

    type_: PlaybackPolicyType = Field(alias="type")
    allowed_origins: list[str] = Field(
        alias="allowedOrigins",
        description="Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied.",
    )
    "Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied."


class RebufferingEventDefaultStreamThumbnailAssets(BaseModel):
    """Chandler-served thumbnail asset URLs (poster, sprite, VTT cues). URL shape
    is `{chandlerBase}/assets/{assetKey}/poster.jpg`,
    `/assets/{assetKey}/sprite.jpg`, `/assets/{assetKey}/sprite.vtt` — Chandler
    serves the object key directly, no version resolution. assetKey is stream_id
    for live streams; clip_hash / dvr_hash / vod_hash (= artifact_hash) for artifacts."""

    poster_url: str = Field(alias="posterUrl")
    sprite_vtt_url: str = Field(alias="spriteVttUrl")
    sprite_jpg_url: str = Field(alias="spriteJpgUrl")
    asset_key: str = Field(alias="assetKey")


class RebufferingEventDefaultStreamRetentionOverrides(BaseModel):
    """Per-stream retention overrides for DVR and clips. VOD uploads aren't
    stream-bound, so they have no stream-level knob here."""

    stream_id: str = Field(alias="streamId")
    dvr_retention_days_override: Optional[int] = Field(
        alias="dvrRetentionDaysOverride",
        description="Null = no override (inherit tenant default). 0 = no auto-expire.",
    )
    "Null = no override (inherit tenant default). 0 = no auto-expire."
    clip_retention_days_override: Optional[int] = Field(
        alias="clipRetentionDaysOverride"
    )


class RoutingEfficiencyDefault(BaseModel):
    """Pre-aggregated routing efficiency summary (replaces client-side aggregation of raw routing events)."""

    total_decisions: int = Field(alias="totalDecisions")
    success_count: int = Field(alias="successCount")
    success_rate: float = Field(alias="successRate")
    avg_routing_distance: float = Field(alias="avgRoutingDistance")
    avg_latency_ms: float = Field(alias="avgLatencyMs")
    top_countries: list["RoutingEfficiencyDefaultTopCountries"] = Field(
        alias="topCountries"
    )


class RoutingEfficiencyDefaultTopCountries(BaseModel):
    country_code: str = Field(alias="countryCode")
    request_count: int = Field(alias="requestCount")


class RoutingEventDefault(BaseModel):
    timestamp: datetime
    stream_id: str = Field(alias="streamId")
    stream: Optional["RoutingEventDefaultStream"]
    selected_node: str = Field(alias="selectedNode")
    node_id: Optional[str] = Field(alias="nodeId")
    status: str
    details: Optional[str]
    score: Optional[int]
    client_country: Optional[str] = Field(alias="clientCountry")
    client_latitude: Optional[float] = Field(alias="clientLatitude")
    client_longitude: Optional[float] = Field(alias="clientLongitude")
    client_bucket: Optional["RoutingEventDefaultClientBucket"] = Field(
        alias="clientBucket"
    )
    node_latitude: Optional[float] = Field(alias="nodeLatitude")
    node_longitude: Optional[float] = Field(alias="nodeLongitude")
    node_name: Optional[str] = Field(alias="nodeName")
    node_bucket: Optional["RoutingEventDefaultNodeBucket"] = Field(alias="nodeBucket")
    routing_distance: Optional[float] = Field(alias="routingDistance")
    candidates_count: Optional[int] = Field(alias="candidatesCount")
    latency_ms: Optional[float] = Field(alias="latencyMs")
    event_type: Optional[str] = Field(alias="eventType")
    source: Optional[str]
    stream_tenant_id: Optional[str] = Field(alias="streamTenantId")
    cluster_id: Optional[str] = Field(alias="clusterId")
    remote_cluster_id: Optional[str] = Field(alias="remoteClusterId")
    selected_cluster_id: Optional[str] = Field(alias="selectedClusterId")
    control_cell_id: Optional[str] = Field(alias="controlCellId")
    origin_cluster_id: Optional[str] = Field(alias="originClusterId")


class RoutingEventDefaultStream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

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
        description="Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path.",
    )
    "Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path."
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
    pull_source: Optional["RoutingEventDefaultStreamPullSource"] = Field(
        alias="pullSource",
        description="Pull-source config for pull streams; null for push streams.",
    )
    "Pull-source config for pull streams; null for push streams."
    managed_source: Optional["RoutingEventDefaultStreamManagedSource"] = Field(
        alias="managedSource",
        description="Safe source summary for managed streams; null for push and pull streams.",
    )
    "Safe source summary for managed streams; null for push and pull streams."
    source_location: "RoutingEventDefaultStreamSourceLocation" = Field(
        alias="sourceLocation",
        description="Where the stream's source may be ingested, derived from the stream's own ingest placement rules.",
    )
    "Where the stream's source may be ingested, derived from the stream's own ingest placement rules."
    created_at: datetime = Field(
        alias="createdAt", description="When this stream was created."
    )
    "When this stream was created."
    updated_at: datetime = Field(
        alias="updatedAt", description="When this stream was last modified."
    )
    "When this stream was last modified."
    metrics: Optional["RoutingEventDefaultStreamMetrics"] = Field(
        description="Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    )
    "Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    push_targets: list["RoutingEventDefaultStreamPushTargets"] = Field(
        alias="pushTargets",
        description="Configured multistream push targets for this stream.",
    )
    "Configured multistream push targets for this stream."
    playback_policy: Optional["RoutingEventDefaultStreamPlaybackPolicy"] = Field(
        alias="playbackPolicy",
        description="Playback access policy. null/PUBLIC = anyone with the playbackId can watch.",
    )
    "Playback access policy. null/PUBLIC = anyone with the playbackId can watch."
    dvr_chapter_mode: Optional[DVRChapterMode] = Field(
        alias="dvrChapterMode",
        description="How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast.",
    )
    "How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast."
    dvr_chapter_interval_seconds: Optional[int] = Field(
        alias="dvrChapterIntervalSeconds",
        description="Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour).",
    )
    "Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour)."
    monitoring: MonitoringToggle = Field(
        description="Per-stream Skipper monitoring override (INHERIT follows tier)."
    )
    "Per-stream Skipper monitoring override (INHERIT follows tier)."
    thumbnail_assets: Optional["RoutingEventDefaultStreamThumbnailAssets"] = Field(
        alias="thumbnailAssets",
        description="Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles.",
    )
    "Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles."
    retention_overrides: Optional["RoutingEventDefaultStreamRetentionOverrides"] = (
        Field(
            alias="retentionOverrides",
            description="Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here.",
        )
    )
    "Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here."


class RoutingEventDefaultStreamPullSource(BaseModel):
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


class RoutingEventDefaultStreamManagedSource(BaseModel):
    """Safe summary of an operator-managed source. Literal paths, commands, and
    credentials are intentionally not exposed through the tenant API."""

    source_kind: str = Field(
        alias="sourceKind", description="Managed source form: file, playlist, or exec."
    )
    "Managed source form: file, playlist, or exec."
    always_on: bool = Field(
        alias="alwaysOn",
        description="Whether the source is kept active without waiting for a viewer.",
    )
    "Whether the source is kept active without waiting for a viewer."
    placement_count: int = Field(
        alias="placementCount",
        description="Number of source placements requested by the operator configuration.",
    )
    "Number of source placements requested by the operator configuration."


class RoutingEventDefaultStreamSourceLocation(BaseModel):
    mode: SourceLocationMode
    avoid_node_ids: list[str] = Field(
        alias="avoidNodeIds", description="Empty unless mode is RESTRICTED."
    )
    "Empty unless mode is RESTRICTED."


class RoutingEventDefaultStreamMetrics(BaseModel):
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
    node_id: Optional[str] = Field(
        alias="nodeId", description="ID of the edge node handling this stream."
    )
    "ID of the edge node handling this stream."
    track_count: Optional[int] = Field(
        alias="trackCount", description="Number of quality tracks available."
    )
    "Number of quality tracks available."
    total_inputs: Optional[int] = Field(
        alias="totalInputs", description="Total ingest connections to this stream."
    )
    "Total ingest connections to this stream."
    uploaded_bytes: float = Field(
        alias="uploadedBytes",
        description="Total bytes uploaded (ingested) for current session.",
    )
    "Total bytes uploaded (ingested) for current session."
    downloaded_bytes: float = Field(
        alias="downloadedBytes",
        description="Total bytes downloaded (egress) for current session.",
    )
    "Total bytes downloaded (egress) for current session."
    viewer_seconds: float = Field(
        alias="viewerSeconds", description="Total viewer-seconds accumulated."
    )
    "Total viewer-seconds accumulated."
    packets_sent: Optional[float] = Field(
        alias="packetsSent", description="Total packets sent to viewers."
    )
    "Total packets sent to viewers."
    packets_lost: Optional[float] = Field(
        alias="packetsLost", description="Total packets lost in transit."
    )
    "Total packets lost in transit."
    packets_retransmitted: Optional[float] = Field(
        alias="packetsRetransmitted", description="Total packets retransmitted."
    )
    "Total packets retransmitted."
    buffer_state: Optional[str] = Field(
        alias="bufferState",
        description="Buffer health state (HEALTHY, WARNING, CRITICAL).",
    )
    "Buffer health state (HEALTHY, WARNING, CRITICAL)."
    quality_tier: Optional[str] = Field(
        alias="qualityTier",
        description="Highest quality tier available (4K, 1080p, 720p, etc.).",
    )
    "Highest quality tier available (4K, 1080p, 720p, etc.)."
    primary_width: Optional[int] = Field(
        alias="primaryWidth", description="Primary video track width in pixels."
    )
    "Primary video track width in pixels."
    primary_height: Optional[int] = Field(
        alias="primaryHeight", description="Primary video track height in pixels."
    )
    "Primary video track height in pixels."
    primary_fps: Optional[float] = Field(
        alias="primaryFps", description="Primary video track framerate."
    )
    "Primary video track framerate."
    primary_codec: Optional[str] = Field(
        alias="primaryCodec",
        description="Primary video codec (H.264, H.265, VP9, AV1).",
    )
    "Primary video codec (H.264, H.265, VP9, AV1)."
    primary_bitrate: Optional[int] = Field(
        alias="primaryBitrate", description="Primary video bitrate in kbps."
    )
    "Primary video bitrate in kbps."
    has_issues: Optional[bool] = Field(
        alias="hasIssues", description="Whether the stream has active quality issues."
    )
    "Whether the stream has active quality issues."
    issues_description: Optional[str] = Field(
        alias="issuesDescription",
        description="Human-readable description of current issues.",
    )
    "Human-readable description of current issues."


class RoutingEventDefaultStreamPushTargets(BaseModel):
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


class RoutingEventDefaultStreamPlaybackPolicy(BaseModel):
    """Per-playback-object access policy. Foghorn reads this in the USER_NEW
    trigger handler; the requiresAuth marker (on the playback object itself)
    gates whether the full policy is fetched at all."""

    type_: PlaybackPolicyType = Field(alias="type")
    allowed_origins: list[str] = Field(
        alias="allowedOrigins",
        description="Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied.",
    )
    "Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied."


class RoutingEventDefaultStreamThumbnailAssets(BaseModel):
    """Chandler-served thumbnail asset URLs (poster, sprite, VTT cues). URL shape
    is `{chandlerBase}/assets/{assetKey}/poster.jpg`,
    `/assets/{assetKey}/sprite.jpg`, `/assets/{assetKey}/sprite.vtt` — Chandler
    serves the object key directly, no version resolution. assetKey is stream_id
    for live streams; clip_hash / dvr_hash / vod_hash (= artifact_hash) for artifacts."""

    poster_url: str = Field(alias="posterUrl")
    sprite_vtt_url: str = Field(alias="spriteVttUrl")
    sprite_jpg_url: str = Field(alias="spriteJpgUrl")
    asset_key: str = Field(alias="assetKey")


class RoutingEventDefaultStreamRetentionOverrides(BaseModel):
    """Per-stream retention overrides for DVR and clips. VOD uploads aren't
    stream-bound, so they have no stream-level knob here."""

    stream_id: str = Field(alias="streamId")
    dvr_retention_days_override: Optional[int] = Field(
        alias="dvrRetentionDaysOverride",
        description="Null = no override (inherit tenant default). 0 = no auto-expire.",
    )
    "Null = no override (inherit tenant default). 0 = no auto-expire."
    clip_retention_days_override: Optional[int] = Field(
        alias="clipRetentionDaysOverride"
    )


class RoutingEventDefaultClientBucket(BaseModel):
    h_3_index: str = Field(alias="h3Index")
    resolution: int


class RoutingEventDefaultNodeBucket(BaseModel):
    h_3_index: str = Field(alias="h3Index")
    resolution: int


class ServiceInstanceDefault(BaseModel):
    id: str
    instance_id: str = Field(alias="instanceId")
    cluster_id: str = Field(alias="clusterId")
    node_id: Optional[str] = Field(alias="nodeId")
    service_id: str = Field(alias="serviceId")
    version: Optional[str]
    port: Optional[int]
    process_id: Optional[int] = Field(alias="processId")
    container_id: Optional[str] = Field(alias="containerId")
    status: InstanceStatus
    health_status: NodeStatus = Field(alias="healthStatus")
    started_at: Optional[datetime] = Field(alias="startedAt")
    stopped_at: Optional[datetime] = Field(alias="stoppedAt")
    last_health_check: Optional[datetime] = Field(alias="lastHealthCheck")


class ServiceInstanceHealthDefault(BaseModel):
    instance_id: str = Field(alias="instanceId")
    service_id: str = Field(alias="serviceId")
    cluster_id: str = Field(alias="clusterId")
    protocol: str
    host: Optional[str]
    port: int
    health_endpoint: Optional[str] = Field(alias="healthEndpoint")
    status: str
    last_health_check: Optional[datetime] = Field(alias="lastHealthCheck")


class SessionQoeSummaryDefault(BaseModel):
    """Viewer-experienced QoE summary. Ratios are sum(numerator)/sum(denominator) over
    the player-reported session beacons; rebufferingRatio is the headline QoE number."""

    session_count: int = Field(alias="sessionCount")
    played_hours: float = Field(alias="playedHours")
    rebuffering_ratio: float = Field(alias="rebufferingRatio")
    rebuffers_per_hour: float = Field(alias="rebuffersPerHour")
    avg_rebuffer_ms: float = Field(alias="avgRebufferMs")
    frame_drop_ratio: float = Field(alias="frameDropRatio")
    playback_failure_rate: float = Field(alias="playbackFailureRate")
    ebvs_rate: float = Field(alias="ebvsRate")
    avg_bitrate_bps: float = Field(alias="avgBitrateBps")
    abr_switches_per_hour: float = Field(alias="abrSwitchesPerHour")
    avg_live_edge_latency_ms: float = Field(alias="avgLiveEdgeLatencyMs")


class SessionQoeTimeSeriesBucketDefault(BaseModel):
    """One toStartOfInterval window of the viewer-experienced QoE summary. Ratios are
    sum(numerator)/sum(denominator) over the per-session rollup; `sessionCount` and
    `playedHours` are the per-bucket denominators."""

    timestamp: datetime
    session_count: int = Field(alias="sessionCount")
    played_hours: float = Field(alias="playedHours")
    rebuffering_ratio: float = Field(alias="rebufferingRatio")
    frame_drop_ratio: float = Field(alias="frameDropRatio")
    avg_bitrate_bps: float = Field(alias="avgBitrateBps")


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


class SigningKeyInNodeDefault(BaseModel):
    """A customer-managed signing key for issuing viewer playback JWTs. The private
    key is returned exactly once at creation time (in CreateSigningKeySuccess);
    FrameWorks stores only the public key. Up to 10 active keys per tenant."""

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
    signing_key_status: SigningKeyStatus = Field(
        alias="signingKeyStatus", description="Lifecycle status."
    )
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


class SkipperConversationDefault(BaseModel):
    id: str
    title: str
    messages: list["SkipperConversationDefaultMessages"]
    created_at: datetime = Field(alias="createdAt")
    updated_at: datetime = Field(alias="updatedAt")


class SkipperConversationDefaultMessages(BaseModel):
    id: str
    role: str
    content: str
    confidence: Optional[str]
    sources: Optional[Any]
    tools_used: Optional[Any] = Field(alias="toolsUsed")
    confidence_blocks: Optional[Any] = Field(alias="confidenceBlocks")
    tokens_input: int = Field(alias="tokensInput")
    tokens_output: int = Field(alias="tokensOutput")
    created_at: datetime = Field(alias="createdAt")


class SkipperConversationSummaryDefault(BaseModel):
    id: str
    title: str
    created_at: datetime = Field(alias="createdAt")
    updated_at: datetime = Field(alias="updatedAt")


class SkipperDoneDefault(BaseModel):
    conversation_id: str = Field(alias="conversationId")
    tokens_input: int = Field(alias="tokensInput")
    tokens_output: int = Field(alias="tokensOutput")


class SkipperMetaDefault(BaseModel):
    confidence: str
    citations: list["SkipperMetaDefaultCitations"]
    external_links: list["SkipperMetaDefaultExternalLinks"] = Field(
        alias="externalLinks"
    )
    details: list["SkipperMetaDefaultDetails"]
    blocks: Optional[list["SkipperMetaDefaultBlocks"]]


class SkipperMetaDefaultCitations(BaseModel):
    label: str
    url: str


class SkipperMetaDefaultExternalLinks(BaseModel):
    label: str
    url: str


class SkipperMetaDefaultDetails(BaseModel):
    title: str
    payload: Any


class SkipperMetaDefaultBlocks(BaseModel):
    content: str
    confidence: str
    sources: list["SkipperMetaDefaultBlocksSources"]


class SkipperMetaDefaultBlocksSources(BaseModel):
    label: str
    url: str


class SkipperReportDefault(BaseModel):
    id: str
    trigger: str
    summary: str
    metrics_reviewed: list[str] = Field(alias="metricsReviewed")
    root_cause: str = Field(alias="rootCause")
    recommendations: list["SkipperReportDefaultRecommendations"]
    created_at: datetime = Field(alias="createdAt")
    read_at: Optional[datetime] = Field(alias="readAt")


class SkipperReportDefaultRecommendations(BaseModel):
    text: str
    confidence: str


class SkipperReportsConnectionDefault(BaseModel):
    nodes: list["SkipperReportsConnectionDefaultNodes"]
    total_count: int = Field(alias="totalCount")
    unread_count: int = Field(alias="unreadCount")


class SkipperReportsConnectionDefaultNodes(BaseModel):
    id: str
    trigger: str
    summary: str
    metrics_reviewed: list[str] = Field(alias="metricsReviewed")
    root_cause: str = Field(alias="rootCause")
    recommendations: list["SkipperReportsConnectionDefaultNodesRecommendations"]
    created_at: datetime = Field(alias="createdAt")
    read_at: Optional[datetime] = Field(alias="readAt")


class SkipperReportsConnectionDefaultNodesRecommendations(BaseModel):
    text: str
    confidence: str


class SkipperTokenDefault(BaseModel):
    content: str


class SkipperToolEndEventDefault(BaseModel):
    tool: str
    error: Optional[str]


class SkipperToolStartEventDefault(BaseModel):
    tool: str


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


class StorageEventDefault(BaseModel):
    id: str
    timestamp: datetime
    stream_id: str = Field(alias="streamId")
    stream: Optional["StorageEventDefaultStream"]
    asset_hash: str = Field(alias="assetHash")
    action: str
    asset_type: str = Field(alias="assetType")
    size_bytes: float = Field(alias="sizeBytes")
    s_3_url: Optional[str] = Field(alias="s3Url")
    local_path: Optional[str] = Field(alias="localPath")
    node_id: str = Field(alias="nodeId")
    cluster_id: Optional[str] = Field(alias="clusterId")
    origin_cluster_id: Optional[str] = Field(alias="originClusterId")
    control_cell_id: Optional[str] = Field(alias="controlCellId")
    duration_ms: Optional[int] = Field(alias="durationMs")
    warm_duration_ms: Optional[int] = Field(alias="warmDurationMs")
    error: Optional[str]


class StorageEventDefaultStream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

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
        description="Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path.",
    )
    "Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path."
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
    pull_source: Optional["StorageEventDefaultStreamPullSource"] = Field(
        alias="pullSource",
        description="Pull-source config for pull streams; null for push streams.",
    )
    "Pull-source config for pull streams; null for push streams."
    managed_source: Optional["StorageEventDefaultStreamManagedSource"] = Field(
        alias="managedSource",
        description="Safe source summary for managed streams; null for push and pull streams.",
    )
    "Safe source summary for managed streams; null for push and pull streams."
    source_location: "StorageEventDefaultStreamSourceLocation" = Field(
        alias="sourceLocation",
        description="Where the stream's source may be ingested, derived from the stream's own ingest placement rules.",
    )
    "Where the stream's source may be ingested, derived from the stream's own ingest placement rules."
    created_at: datetime = Field(
        alias="createdAt", description="When this stream was created."
    )
    "When this stream was created."
    updated_at: datetime = Field(
        alias="updatedAt", description="When this stream was last modified."
    )
    "When this stream was last modified."
    metrics: Optional["StorageEventDefaultStreamMetrics"] = Field(
        description="Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    )
    "Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    push_targets: list["StorageEventDefaultStreamPushTargets"] = Field(
        alias="pushTargets",
        description="Configured multistream push targets for this stream.",
    )
    "Configured multistream push targets for this stream."
    playback_policy: Optional["StorageEventDefaultStreamPlaybackPolicy"] = Field(
        alias="playbackPolicy",
        description="Playback access policy. null/PUBLIC = anyone with the playbackId can watch.",
    )
    "Playback access policy. null/PUBLIC = anyone with the playbackId can watch."
    dvr_chapter_mode: Optional[DVRChapterMode] = Field(
        alias="dvrChapterMode",
        description="How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast.",
    )
    "How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast."
    dvr_chapter_interval_seconds: Optional[int] = Field(
        alias="dvrChapterIntervalSeconds",
        description="Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour).",
    )
    "Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour)."
    monitoring: MonitoringToggle = Field(
        description="Per-stream Skipper monitoring override (INHERIT follows tier)."
    )
    "Per-stream Skipper monitoring override (INHERIT follows tier)."
    thumbnail_assets: Optional["StorageEventDefaultStreamThumbnailAssets"] = Field(
        alias="thumbnailAssets",
        description="Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles.",
    )
    "Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles."
    retention_overrides: Optional["StorageEventDefaultStreamRetentionOverrides"] = (
        Field(
            alias="retentionOverrides",
            description="Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here.",
        )
    )
    "Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here."


class StorageEventDefaultStreamPullSource(BaseModel):
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


class StorageEventDefaultStreamManagedSource(BaseModel):
    """Safe summary of an operator-managed source. Literal paths, commands, and
    credentials are intentionally not exposed through the tenant API."""

    source_kind: str = Field(
        alias="sourceKind", description="Managed source form: file, playlist, or exec."
    )
    "Managed source form: file, playlist, or exec."
    always_on: bool = Field(
        alias="alwaysOn",
        description="Whether the source is kept active without waiting for a viewer.",
    )
    "Whether the source is kept active without waiting for a viewer."
    placement_count: int = Field(
        alias="placementCount",
        description="Number of source placements requested by the operator configuration.",
    )
    "Number of source placements requested by the operator configuration."


class StorageEventDefaultStreamSourceLocation(BaseModel):
    mode: SourceLocationMode
    avoid_node_ids: list[str] = Field(
        alias="avoidNodeIds", description="Empty unless mode is RESTRICTED."
    )
    "Empty unless mode is RESTRICTED."


class StorageEventDefaultStreamMetrics(BaseModel):
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
    node_id: Optional[str] = Field(
        alias="nodeId", description="ID of the edge node handling this stream."
    )
    "ID of the edge node handling this stream."
    track_count: Optional[int] = Field(
        alias="trackCount", description="Number of quality tracks available."
    )
    "Number of quality tracks available."
    total_inputs: Optional[int] = Field(
        alias="totalInputs", description="Total ingest connections to this stream."
    )
    "Total ingest connections to this stream."
    uploaded_bytes: float = Field(
        alias="uploadedBytes",
        description="Total bytes uploaded (ingested) for current session.",
    )
    "Total bytes uploaded (ingested) for current session."
    downloaded_bytes: float = Field(
        alias="downloadedBytes",
        description="Total bytes downloaded (egress) for current session.",
    )
    "Total bytes downloaded (egress) for current session."
    viewer_seconds: float = Field(
        alias="viewerSeconds", description="Total viewer-seconds accumulated."
    )
    "Total viewer-seconds accumulated."
    packets_sent: Optional[float] = Field(
        alias="packetsSent", description="Total packets sent to viewers."
    )
    "Total packets sent to viewers."
    packets_lost: Optional[float] = Field(
        alias="packetsLost", description="Total packets lost in transit."
    )
    "Total packets lost in transit."
    packets_retransmitted: Optional[float] = Field(
        alias="packetsRetransmitted", description="Total packets retransmitted."
    )
    "Total packets retransmitted."
    buffer_state: Optional[str] = Field(
        alias="bufferState",
        description="Buffer health state (HEALTHY, WARNING, CRITICAL).",
    )
    "Buffer health state (HEALTHY, WARNING, CRITICAL)."
    quality_tier: Optional[str] = Field(
        alias="qualityTier",
        description="Highest quality tier available (4K, 1080p, 720p, etc.).",
    )
    "Highest quality tier available (4K, 1080p, 720p, etc.)."
    primary_width: Optional[int] = Field(
        alias="primaryWidth", description="Primary video track width in pixels."
    )
    "Primary video track width in pixels."
    primary_height: Optional[int] = Field(
        alias="primaryHeight", description="Primary video track height in pixels."
    )
    "Primary video track height in pixels."
    primary_fps: Optional[float] = Field(
        alias="primaryFps", description="Primary video track framerate."
    )
    "Primary video track framerate."
    primary_codec: Optional[str] = Field(
        alias="primaryCodec",
        description="Primary video codec (H.264, H.265, VP9, AV1).",
    )
    "Primary video codec (H.264, H.265, VP9, AV1)."
    primary_bitrate: Optional[int] = Field(
        alias="primaryBitrate", description="Primary video bitrate in kbps."
    )
    "Primary video bitrate in kbps."
    has_issues: Optional[bool] = Field(
        alias="hasIssues", description="Whether the stream has active quality issues."
    )
    "Whether the stream has active quality issues."
    issues_description: Optional[str] = Field(
        alias="issuesDescription",
        description="Human-readable description of current issues.",
    )
    "Human-readable description of current issues."


class StorageEventDefaultStreamPushTargets(BaseModel):
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


class StorageEventDefaultStreamPlaybackPolicy(BaseModel):
    """Per-playback-object access policy. Foghorn reads this in the USER_NEW
    trigger handler; the requiresAuth marker (on the playback object itself)
    gates whether the full policy is fetched at all."""

    type_: PlaybackPolicyType = Field(alias="type")
    allowed_origins: list[str] = Field(
        alias="allowedOrigins",
        description="Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied.",
    )
    "Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied."


class StorageEventDefaultStreamThumbnailAssets(BaseModel):
    """Chandler-served thumbnail asset URLs (poster, sprite, VTT cues). URL shape
    is `{chandlerBase}/assets/{assetKey}/poster.jpg`,
    `/assets/{assetKey}/sprite.jpg`, `/assets/{assetKey}/sprite.vtt` — Chandler
    serves the object key directly, no version resolution. assetKey is stream_id
    for live streams; clip_hash / dvr_hash / vod_hash (= artifact_hash) for artifacts."""

    poster_url: str = Field(alias="posterUrl")
    sprite_vtt_url: str = Field(alias="spriteVttUrl")
    sprite_jpg_url: str = Field(alias="spriteJpgUrl")
    asset_key: str = Field(alias="assetKey")


class StorageEventDefaultStreamRetentionOverrides(BaseModel):
    """Per-stream retention overrides for DVR and clips. VOD uploads aren't
    stream-bound, so they have no stream-level knob here."""

    stream_id: str = Field(alias="streamId")
    dvr_retention_days_override: Optional[int] = Field(
        alias="dvrRetentionDaysOverride",
        description="Null = no override (inherit tenant default). 0 = no auto-expire.",
    )
    "Null = no override (inherit tenant default). 0 = no auto-expire."
    clip_retention_days_override: Optional[int] = Field(
        alias="clipRetentionDaysOverride"
    )


class StorageEventInNodeDefault(BaseModel):
    id: str
    timestamp: datetime
    stream_id: str = Field(alias="streamId")
    stream: Optional["StorageEventInNodeDefaultStream"]
    asset_hash: str = Field(alias="assetHash")
    action: str
    asset_type: str = Field(alias="assetType")
    storage_event_size_bytes: float = Field(alias="storageEventSizeBytes")
    s_3_url: Optional[str] = Field(alias="s3Url")
    local_path: Optional[str] = Field(alias="localPath")
    node_id: str = Field(alias="nodeId")
    cluster_id: Optional[str] = Field(alias="clusterId")
    origin_cluster_id: Optional[str] = Field(alias="originClusterId")
    control_cell_id: Optional[str] = Field(alias="controlCellId")
    duration_ms: Optional[int] = Field(alias="durationMs")
    warm_duration_ms: Optional[int] = Field(alias="warmDurationMs")
    error: Optional[str]


class StorageEventInNodeDefaultStream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

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
        description="Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path.",
    )
    "Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path."
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
    pull_source: Optional["StorageEventInNodeDefaultStreamPullSource"] = Field(
        alias="pullSource",
        description="Pull-source config for pull streams; null for push streams.",
    )
    "Pull-source config for pull streams; null for push streams."
    managed_source: Optional["StorageEventInNodeDefaultStreamManagedSource"] = Field(
        alias="managedSource",
        description="Safe source summary for managed streams; null for push and pull streams.",
    )
    "Safe source summary for managed streams; null for push and pull streams."
    source_location: "StorageEventInNodeDefaultStreamSourceLocation" = Field(
        alias="sourceLocation",
        description="Where the stream's source may be ingested, derived from the stream's own ingest placement rules.",
    )
    "Where the stream's source may be ingested, derived from the stream's own ingest placement rules."
    created_at: datetime = Field(
        alias="createdAt", description="When this stream was created."
    )
    "When this stream was created."
    updated_at: datetime = Field(
        alias="updatedAt", description="When this stream was last modified."
    )
    "When this stream was last modified."
    metrics: Optional["StorageEventInNodeDefaultStreamMetrics"] = Field(
        description="Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    )
    "Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    push_targets: list["StorageEventInNodeDefaultStreamPushTargets"] = Field(
        alias="pushTargets",
        description="Configured multistream push targets for this stream.",
    )
    "Configured multistream push targets for this stream."
    playback_policy: Optional["StorageEventInNodeDefaultStreamPlaybackPolicy"] = Field(
        alias="playbackPolicy",
        description="Playback access policy. null/PUBLIC = anyone with the playbackId can watch.",
    )
    "Playback access policy. null/PUBLIC = anyone with the playbackId can watch."
    dvr_chapter_mode: Optional[DVRChapterMode] = Field(
        alias="dvrChapterMode",
        description="How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast.",
    )
    "How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast."
    dvr_chapter_interval_seconds: Optional[int] = Field(
        alias="dvrChapterIntervalSeconds",
        description="Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour).",
    )
    "Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour)."
    monitoring: MonitoringToggle = Field(
        description="Per-stream Skipper monitoring override (INHERIT follows tier)."
    )
    "Per-stream Skipper monitoring override (INHERIT follows tier)."
    thumbnail_assets: Optional["StorageEventInNodeDefaultStreamThumbnailAssets"] = (
        Field(
            alias="thumbnailAssets",
            description="Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles.",
        )
    )
    "Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles."
    retention_overrides: Optional[
        "StorageEventInNodeDefaultStreamRetentionOverrides"
    ] = Field(
        alias="retentionOverrides",
        description="Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here.",
    )
    "Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here."


class StorageEventInNodeDefaultStreamPullSource(BaseModel):
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


class StorageEventInNodeDefaultStreamManagedSource(BaseModel):
    """Safe summary of an operator-managed source. Literal paths, commands, and
    credentials are intentionally not exposed through the tenant API."""

    source_kind: str = Field(
        alias="sourceKind", description="Managed source form: file, playlist, or exec."
    )
    "Managed source form: file, playlist, or exec."
    always_on: bool = Field(
        alias="alwaysOn",
        description="Whether the source is kept active without waiting for a viewer.",
    )
    "Whether the source is kept active without waiting for a viewer."
    placement_count: int = Field(
        alias="placementCount",
        description="Number of source placements requested by the operator configuration.",
    )
    "Number of source placements requested by the operator configuration."


class StorageEventInNodeDefaultStreamSourceLocation(BaseModel):
    mode: SourceLocationMode
    avoid_node_ids: list[str] = Field(
        alias="avoidNodeIds", description="Empty unless mode is RESTRICTED."
    )
    "Empty unless mode is RESTRICTED."


class StorageEventInNodeDefaultStreamMetrics(BaseModel):
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
    node_id: Optional[str] = Field(
        alias="nodeId", description="ID of the edge node handling this stream."
    )
    "ID of the edge node handling this stream."
    track_count: Optional[int] = Field(
        alias="trackCount", description="Number of quality tracks available."
    )
    "Number of quality tracks available."
    total_inputs: Optional[int] = Field(
        alias="totalInputs", description="Total ingest connections to this stream."
    )
    "Total ingest connections to this stream."
    uploaded_bytes: float = Field(
        alias="uploadedBytes",
        description="Total bytes uploaded (ingested) for current session.",
    )
    "Total bytes uploaded (ingested) for current session."
    downloaded_bytes: float = Field(
        alias="downloadedBytes",
        description="Total bytes downloaded (egress) for current session.",
    )
    "Total bytes downloaded (egress) for current session."
    viewer_seconds: float = Field(
        alias="viewerSeconds", description="Total viewer-seconds accumulated."
    )
    "Total viewer-seconds accumulated."
    packets_sent: Optional[float] = Field(
        alias="packetsSent", description="Total packets sent to viewers."
    )
    "Total packets sent to viewers."
    packets_lost: Optional[float] = Field(
        alias="packetsLost", description="Total packets lost in transit."
    )
    "Total packets lost in transit."
    packets_retransmitted: Optional[float] = Field(
        alias="packetsRetransmitted", description="Total packets retransmitted."
    )
    "Total packets retransmitted."
    buffer_state: Optional[str] = Field(
        alias="bufferState",
        description="Buffer health state (HEALTHY, WARNING, CRITICAL).",
    )
    "Buffer health state (HEALTHY, WARNING, CRITICAL)."
    quality_tier: Optional[str] = Field(
        alias="qualityTier",
        description="Highest quality tier available (4K, 1080p, 720p, etc.).",
    )
    "Highest quality tier available (4K, 1080p, 720p, etc.)."
    primary_width: Optional[int] = Field(
        alias="primaryWidth", description="Primary video track width in pixels."
    )
    "Primary video track width in pixels."
    primary_height: Optional[int] = Field(
        alias="primaryHeight", description="Primary video track height in pixels."
    )
    "Primary video track height in pixels."
    primary_fps: Optional[float] = Field(
        alias="primaryFps", description="Primary video track framerate."
    )
    "Primary video track framerate."
    primary_codec: Optional[str] = Field(
        alias="primaryCodec",
        description="Primary video codec (H.264, H.265, VP9, AV1).",
    )
    "Primary video codec (H.264, H.265, VP9, AV1)."
    primary_bitrate: Optional[int] = Field(
        alias="primaryBitrate", description="Primary video bitrate in kbps."
    )
    "Primary video bitrate in kbps."
    has_issues: Optional[bool] = Field(
        alias="hasIssues", description="Whether the stream has active quality issues."
    )
    "Whether the stream has active quality issues."
    issues_description: Optional[str] = Field(
        alias="issuesDescription",
        description="Human-readable description of current issues.",
    )
    "Human-readable description of current issues."


class StorageEventInNodeDefaultStreamPushTargets(BaseModel):
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


class StorageEventInNodeDefaultStreamPlaybackPolicy(BaseModel):
    """Per-playback-object access policy. Foghorn reads this in the USER_NEW
    trigger handler; the requiresAuth marker (on the playback object itself)
    gates whether the full policy is fetched at all."""

    type_: PlaybackPolicyType = Field(alias="type")
    allowed_origins: list[str] = Field(
        alias="allowedOrigins",
        description="Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied.",
    )
    "Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied."


class StorageEventInNodeDefaultStreamThumbnailAssets(BaseModel):
    """Chandler-served thumbnail asset URLs (poster, sprite, VTT cues). URL shape
    is `{chandlerBase}/assets/{assetKey}/poster.jpg`,
    `/assets/{assetKey}/sprite.jpg`, `/assets/{assetKey}/sprite.vtt` — Chandler
    serves the object key directly, no version resolution. assetKey is stream_id
    for live streams; clip_hash / dvr_hash / vod_hash (= artifact_hash) for artifacts."""

    poster_url: str = Field(alias="posterUrl")
    sprite_vtt_url: str = Field(alias="spriteVttUrl")
    sprite_jpg_url: str = Field(alias="spriteJpgUrl")
    asset_key: str = Field(alias="assetKey")


class StorageEventInNodeDefaultStreamRetentionOverrides(BaseModel):
    """Per-stream retention overrides for DVR and clips. VOD uploads aren't
    stream-bound, so they have no stream-level knob here."""

    stream_id: str = Field(alias="streamId")
    dvr_retention_days_override: Optional[int] = Field(
        alias="dvrRetentionDaysOverride",
        description="Null = no override (inherit tenant default). 0 = no auto-expire.",
    )
    "Null = no override (inherit tenant default). 0 = no auto-expire."
    clip_retention_days_override: Optional[int] = Field(
        alias="clipRetentionDaysOverride"
    )


class StorageUsageRecordDefault(BaseModel):
    id: str
    timestamp: datetime
    node_id: str = Field(alias="nodeId")
    storage_scope: str = Field(alias="storageScope")
    total_bytes: float = Field(alias="totalBytes")
    file_count: int = Field(alias="fileCount")
    dvr_bytes: float = Field(alias="dvrBytes")
    clip_bytes: float = Field(alias="clipBytes")
    vod_bytes: float = Field(alias="vodBytes")
    frozen_dvr_bytes: float = Field(alias="frozenDvrBytes")
    frozen_clip_bytes: float = Field(alias="frozenClipBytes")
    frozen_vod_bytes: float = Field(alias="frozenVodBytes")


class StreamAnalyticsDailyDefault(BaseModel):
    id: str
    day: datetime
    stream_id: str = Field(alias="streamId")
    stream: Optional["StreamAnalyticsDailyDefaultStream"]
    total_views: int = Field(alias="totalViews")
    unique_viewers: int = Field(alias="uniqueViewers")
    unique_countries: int = Field(alias="uniqueCountries")
    unique_cities: int = Field(alias="uniqueCities")
    egress_bytes: float = Field(alias="egressBytes")


class StreamAnalyticsDailyDefaultStream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

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
        description="Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path.",
    )
    "Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path."
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
    pull_source: Optional["StreamAnalyticsDailyDefaultStreamPullSource"] = Field(
        alias="pullSource",
        description="Pull-source config for pull streams; null for push streams.",
    )
    "Pull-source config for pull streams; null for push streams."
    managed_source: Optional["StreamAnalyticsDailyDefaultStreamManagedSource"] = Field(
        alias="managedSource",
        description="Safe source summary for managed streams; null for push and pull streams.",
    )
    "Safe source summary for managed streams; null for push and pull streams."
    source_location: "StreamAnalyticsDailyDefaultStreamSourceLocation" = Field(
        alias="sourceLocation",
        description="Where the stream's source may be ingested, derived from the stream's own ingest placement rules.",
    )
    "Where the stream's source may be ingested, derived from the stream's own ingest placement rules."
    created_at: datetime = Field(
        alias="createdAt", description="When this stream was created."
    )
    "When this stream was created."
    updated_at: datetime = Field(
        alias="updatedAt", description="When this stream was last modified."
    )
    "When this stream was last modified."
    metrics: Optional["StreamAnalyticsDailyDefaultStreamMetrics"] = Field(
        description="Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    )
    "Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    push_targets: list["StreamAnalyticsDailyDefaultStreamPushTargets"] = Field(
        alias="pushTargets",
        description="Configured multistream push targets for this stream.",
    )
    "Configured multistream push targets for this stream."
    playback_policy: Optional["StreamAnalyticsDailyDefaultStreamPlaybackPolicy"] = (
        Field(
            alias="playbackPolicy",
            description="Playback access policy. null/PUBLIC = anyone with the playbackId can watch.",
        )
    )
    "Playback access policy. null/PUBLIC = anyone with the playbackId can watch."
    dvr_chapter_mode: Optional[DVRChapterMode] = Field(
        alias="dvrChapterMode",
        description="How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast.",
    )
    "How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast."
    dvr_chapter_interval_seconds: Optional[int] = Field(
        alias="dvrChapterIntervalSeconds",
        description="Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour).",
    )
    "Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour)."
    monitoring: MonitoringToggle = Field(
        description="Per-stream Skipper monitoring override (INHERIT follows tier)."
    )
    "Per-stream Skipper monitoring override (INHERIT follows tier)."
    thumbnail_assets: Optional["StreamAnalyticsDailyDefaultStreamThumbnailAssets"] = (
        Field(
            alias="thumbnailAssets",
            description="Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles.",
        )
    )
    "Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles."
    retention_overrides: Optional[
        "StreamAnalyticsDailyDefaultStreamRetentionOverrides"
    ] = Field(
        alias="retentionOverrides",
        description="Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here.",
    )
    "Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here."


class StreamAnalyticsDailyDefaultStreamPullSource(BaseModel):
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


class StreamAnalyticsDailyDefaultStreamManagedSource(BaseModel):
    """Safe summary of an operator-managed source. Literal paths, commands, and
    credentials are intentionally not exposed through the tenant API."""

    source_kind: str = Field(
        alias="sourceKind", description="Managed source form: file, playlist, or exec."
    )
    "Managed source form: file, playlist, or exec."
    always_on: bool = Field(
        alias="alwaysOn",
        description="Whether the source is kept active without waiting for a viewer.",
    )
    "Whether the source is kept active without waiting for a viewer."
    placement_count: int = Field(
        alias="placementCount",
        description="Number of source placements requested by the operator configuration.",
    )
    "Number of source placements requested by the operator configuration."


class StreamAnalyticsDailyDefaultStreamSourceLocation(BaseModel):
    mode: SourceLocationMode
    avoid_node_ids: list[str] = Field(
        alias="avoidNodeIds", description="Empty unless mode is RESTRICTED."
    )
    "Empty unless mode is RESTRICTED."


class StreamAnalyticsDailyDefaultStreamMetrics(BaseModel):
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
    node_id: Optional[str] = Field(
        alias="nodeId", description="ID of the edge node handling this stream."
    )
    "ID of the edge node handling this stream."
    track_count: Optional[int] = Field(
        alias="trackCount", description="Number of quality tracks available."
    )
    "Number of quality tracks available."
    total_inputs: Optional[int] = Field(
        alias="totalInputs", description="Total ingest connections to this stream."
    )
    "Total ingest connections to this stream."
    uploaded_bytes: float = Field(
        alias="uploadedBytes",
        description="Total bytes uploaded (ingested) for current session.",
    )
    "Total bytes uploaded (ingested) for current session."
    downloaded_bytes: float = Field(
        alias="downloadedBytes",
        description="Total bytes downloaded (egress) for current session.",
    )
    "Total bytes downloaded (egress) for current session."
    viewer_seconds: float = Field(
        alias="viewerSeconds", description="Total viewer-seconds accumulated."
    )
    "Total viewer-seconds accumulated."
    packets_sent: Optional[float] = Field(
        alias="packetsSent", description="Total packets sent to viewers."
    )
    "Total packets sent to viewers."
    packets_lost: Optional[float] = Field(
        alias="packetsLost", description="Total packets lost in transit."
    )
    "Total packets lost in transit."
    packets_retransmitted: Optional[float] = Field(
        alias="packetsRetransmitted", description="Total packets retransmitted."
    )
    "Total packets retransmitted."
    buffer_state: Optional[str] = Field(
        alias="bufferState",
        description="Buffer health state (HEALTHY, WARNING, CRITICAL).",
    )
    "Buffer health state (HEALTHY, WARNING, CRITICAL)."
    quality_tier: Optional[str] = Field(
        alias="qualityTier",
        description="Highest quality tier available (4K, 1080p, 720p, etc.).",
    )
    "Highest quality tier available (4K, 1080p, 720p, etc.)."
    primary_width: Optional[int] = Field(
        alias="primaryWidth", description="Primary video track width in pixels."
    )
    "Primary video track width in pixels."
    primary_height: Optional[int] = Field(
        alias="primaryHeight", description="Primary video track height in pixels."
    )
    "Primary video track height in pixels."
    primary_fps: Optional[float] = Field(
        alias="primaryFps", description="Primary video track framerate."
    )
    "Primary video track framerate."
    primary_codec: Optional[str] = Field(
        alias="primaryCodec",
        description="Primary video codec (H.264, H.265, VP9, AV1).",
    )
    "Primary video codec (H.264, H.265, VP9, AV1)."
    primary_bitrate: Optional[int] = Field(
        alias="primaryBitrate", description="Primary video bitrate in kbps."
    )
    "Primary video bitrate in kbps."
    has_issues: Optional[bool] = Field(
        alias="hasIssues", description="Whether the stream has active quality issues."
    )
    "Whether the stream has active quality issues."
    issues_description: Optional[str] = Field(
        alias="issuesDescription",
        description="Human-readable description of current issues.",
    )
    "Human-readable description of current issues."


class StreamAnalyticsDailyDefaultStreamPushTargets(BaseModel):
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


class StreamAnalyticsDailyDefaultStreamPlaybackPolicy(BaseModel):
    """Per-playback-object access policy. Foghorn reads this in the USER_NEW
    trigger handler; the requiresAuth marker (on the playback object itself)
    gates whether the full policy is fetched at all."""

    type_: PlaybackPolicyType = Field(alias="type")
    allowed_origins: list[str] = Field(
        alias="allowedOrigins",
        description="Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied.",
    )
    "Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied."


class StreamAnalyticsDailyDefaultStreamThumbnailAssets(BaseModel):
    """Chandler-served thumbnail asset URLs (poster, sprite, VTT cues). URL shape
    is `{chandlerBase}/assets/{assetKey}/poster.jpg`,
    `/assets/{assetKey}/sprite.jpg`, `/assets/{assetKey}/sprite.vtt` — Chandler
    serves the object key directly, no version resolution. assetKey is stream_id
    for live streams; clip_hash / dvr_hash / vod_hash (= artifact_hash) for artifacts."""

    poster_url: str = Field(alias="posterUrl")
    sprite_vtt_url: str = Field(alias="spriteVttUrl")
    sprite_jpg_url: str = Field(alias="spriteJpgUrl")
    asset_key: str = Field(alias="assetKey")


class StreamAnalyticsDailyDefaultStreamRetentionOverrides(BaseModel):
    """Per-stream retention overrides for DVR and clips. VOD uploads aren't
    stream-bound, so they have no stream-level knob here."""

    stream_id: str = Field(alias="streamId")
    dvr_retention_days_override: Optional[int] = Field(
        alias="dvrRetentionDaysOverride",
        description="Null = no override (inherit tenant default). 0 = no auto-expire.",
    )
    "Null = no override (inherit tenant default). 0 = no auto-expire."
    clip_retention_days_override: Optional[int] = Field(
        alias="clipRetentionDaysOverride"
    )


class StreamAnalyticsSummaryDefault(BaseModel):
    stream_id: str = Field(alias="streamId")
    stream: Optional["StreamAnalyticsSummaryDefaultStream"]
    time_range: "StreamAnalyticsSummaryDefaultTimeRange" = Field(alias="timeRange")
    range_avg_viewers: float = Field(alias="rangeAvgViewers")
    range_peak_concurrent_viewers: int = Field(alias="rangePeakConcurrentViewers")
    range_total_views: int = Field(alias="rangeTotalViews")
    range_total_sessions: int = Field(alias="rangeTotalSessions")
    range_avg_buffer_health: float = Field(alias="rangeAvgBufferHealth")
    range_avg_bitrate: int = Field(alias="rangeAvgBitrate")
    range_avg_fps: float = Field(alias="rangeAvgFps")
    range_packet_loss_rate: Optional[float] = Field(alias="rangePacketLossRate")
    range_avg_connection_time: Optional[float] = Field(alias="rangeAvgConnectionTime")
    range_viewer_hours: float = Field(alias="rangeViewerHours")
    range_egress_gb: float = Field(alias="rangeEgressGb")
    range_avg_session_seconds: float = Field(alias="rangeAvgSessionSeconds")
    range_avg_bytes_per_session: float = Field(alias="rangeAvgBytesPerSession")
    range_unique_viewers: int = Field(alias="rangeUniqueViewers")
    range_unique_countries: int = Field(alias="rangeUniqueCountries")
    range_rebuffer_count: int = Field(alias="rangeRebufferCount")
    range_issue_count: int = Field(alias="rangeIssueCount")
    range_buffer_dry_count: int = Field(alias="rangeBufferDryCount")
    range_quality: "StreamAnalyticsSummaryDefaultRangeQuality" = Field(
        alias="rangeQuality"
    )
    range_egress_share_percent: Optional[float] = Field(
        alias="rangeEgressSharePercent",
        description="This stream's percentage of tenant total egress (only set in bulk queries).",
    )
    "This stream's percentage of tenant total egress (only set in bulk queries)."
    range_viewer_share_percent: Optional[float] = Field(
        alias="rangeViewerSharePercent",
        description="This stream's percentage of tenant total unique viewers (only set in bulk queries).",
    )
    "This stream's percentage of tenant total unique viewers (only set in bulk queries)."
    range_viewer_hours_share_percent: Optional[float] = Field(
        alias="rangeViewerHoursSharePercent",
        description="This stream's percentage of tenant total viewer hours (only set in bulk queries).",
    )
    "This stream's percentage of tenant total viewer hours (only set in bulk queries)."


class StreamAnalyticsSummaryDefaultStream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

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
        description="Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path.",
    )
    "Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path."
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
    pull_source: Optional["StreamAnalyticsSummaryDefaultStreamPullSource"] = Field(
        alias="pullSource",
        description="Pull-source config for pull streams; null for push streams.",
    )
    "Pull-source config for pull streams; null for push streams."
    managed_source: Optional["StreamAnalyticsSummaryDefaultStreamManagedSource"] = (
        Field(
            alias="managedSource",
            description="Safe source summary for managed streams; null for push and pull streams.",
        )
    )
    "Safe source summary for managed streams; null for push and pull streams."
    source_location: "StreamAnalyticsSummaryDefaultStreamSourceLocation" = Field(
        alias="sourceLocation",
        description="Where the stream's source may be ingested, derived from the stream's own ingest placement rules.",
    )
    "Where the stream's source may be ingested, derived from the stream's own ingest placement rules."
    created_at: datetime = Field(
        alias="createdAt", description="When this stream was created."
    )
    "When this stream was created."
    updated_at: datetime = Field(
        alias="updatedAt", description="When this stream was last modified."
    )
    "When this stream was last modified."
    metrics: Optional["StreamAnalyticsSummaryDefaultStreamMetrics"] = Field(
        description="Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    )
    "Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    push_targets: list["StreamAnalyticsSummaryDefaultStreamPushTargets"] = Field(
        alias="pushTargets",
        description="Configured multistream push targets for this stream.",
    )
    "Configured multistream push targets for this stream."
    playback_policy: Optional["StreamAnalyticsSummaryDefaultStreamPlaybackPolicy"] = (
        Field(
            alias="playbackPolicy",
            description="Playback access policy. null/PUBLIC = anyone with the playbackId can watch.",
        )
    )
    "Playback access policy. null/PUBLIC = anyone with the playbackId can watch."
    dvr_chapter_mode: Optional[DVRChapterMode] = Field(
        alias="dvrChapterMode",
        description="How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast.",
    )
    "How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast."
    dvr_chapter_interval_seconds: Optional[int] = Field(
        alias="dvrChapterIntervalSeconds",
        description="Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour).",
    )
    "Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour)."
    monitoring: MonitoringToggle = Field(
        description="Per-stream Skipper monitoring override (INHERIT follows tier)."
    )
    "Per-stream Skipper monitoring override (INHERIT follows tier)."
    thumbnail_assets: Optional["StreamAnalyticsSummaryDefaultStreamThumbnailAssets"] = (
        Field(
            alias="thumbnailAssets",
            description="Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles.",
        )
    )
    "Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles."
    retention_overrides: Optional[
        "StreamAnalyticsSummaryDefaultStreamRetentionOverrides"
    ] = Field(
        alias="retentionOverrides",
        description="Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here.",
    )
    "Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here."


class StreamAnalyticsSummaryDefaultStreamPullSource(BaseModel):
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


class StreamAnalyticsSummaryDefaultStreamManagedSource(BaseModel):
    """Safe summary of an operator-managed source. Literal paths, commands, and
    credentials are intentionally not exposed through the tenant API."""

    source_kind: str = Field(
        alias="sourceKind", description="Managed source form: file, playlist, or exec."
    )
    "Managed source form: file, playlist, or exec."
    always_on: bool = Field(
        alias="alwaysOn",
        description="Whether the source is kept active without waiting for a viewer.",
    )
    "Whether the source is kept active without waiting for a viewer."
    placement_count: int = Field(
        alias="placementCount",
        description="Number of source placements requested by the operator configuration.",
    )
    "Number of source placements requested by the operator configuration."


class StreamAnalyticsSummaryDefaultStreamSourceLocation(BaseModel):
    mode: SourceLocationMode
    avoid_node_ids: list[str] = Field(
        alias="avoidNodeIds", description="Empty unless mode is RESTRICTED."
    )
    "Empty unless mode is RESTRICTED."


class StreamAnalyticsSummaryDefaultStreamMetrics(BaseModel):
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
    node_id: Optional[str] = Field(
        alias="nodeId", description="ID of the edge node handling this stream."
    )
    "ID of the edge node handling this stream."
    track_count: Optional[int] = Field(
        alias="trackCount", description="Number of quality tracks available."
    )
    "Number of quality tracks available."
    total_inputs: Optional[int] = Field(
        alias="totalInputs", description="Total ingest connections to this stream."
    )
    "Total ingest connections to this stream."
    uploaded_bytes: float = Field(
        alias="uploadedBytes",
        description="Total bytes uploaded (ingested) for current session.",
    )
    "Total bytes uploaded (ingested) for current session."
    downloaded_bytes: float = Field(
        alias="downloadedBytes",
        description="Total bytes downloaded (egress) for current session.",
    )
    "Total bytes downloaded (egress) for current session."
    viewer_seconds: float = Field(
        alias="viewerSeconds", description="Total viewer-seconds accumulated."
    )
    "Total viewer-seconds accumulated."
    packets_sent: Optional[float] = Field(
        alias="packetsSent", description="Total packets sent to viewers."
    )
    "Total packets sent to viewers."
    packets_lost: Optional[float] = Field(
        alias="packetsLost", description="Total packets lost in transit."
    )
    "Total packets lost in transit."
    packets_retransmitted: Optional[float] = Field(
        alias="packetsRetransmitted", description="Total packets retransmitted."
    )
    "Total packets retransmitted."
    buffer_state: Optional[str] = Field(
        alias="bufferState",
        description="Buffer health state (HEALTHY, WARNING, CRITICAL).",
    )
    "Buffer health state (HEALTHY, WARNING, CRITICAL)."
    quality_tier: Optional[str] = Field(
        alias="qualityTier",
        description="Highest quality tier available (4K, 1080p, 720p, etc.).",
    )
    "Highest quality tier available (4K, 1080p, 720p, etc.)."
    primary_width: Optional[int] = Field(
        alias="primaryWidth", description="Primary video track width in pixels."
    )
    "Primary video track width in pixels."
    primary_height: Optional[int] = Field(
        alias="primaryHeight", description="Primary video track height in pixels."
    )
    "Primary video track height in pixels."
    primary_fps: Optional[float] = Field(
        alias="primaryFps", description="Primary video track framerate."
    )
    "Primary video track framerate."
    primary_codec: Optional[str] = Field(
        alias="primaryCodec",
        description="Primary video codec (H.264, H.265, VP9, AV1).",
    )
    "Primary video codec (H.264, H.265, VP9, AV1)."
    primary_bitrate: Optional[int] = Field(
        alias="primaryBitrate", description="Primary video bitrate in kbps."
    )
    "Primary video bitrate in kbps."
    has_issues: Optional[bool] = Field(
        alias="hasIssues", description="Whether the stream has active quality issues."
    )
    "Whether the stream has active quality issues."
    issues_description: Optional[str] = Field(
        alias="issuesDescription",
        description="Human-readable description of current issues.",
    )
    "Human-readable description of current issues."


class StreamAnalyticsSummaryDefaultStreamPushTargets(BaseModel):
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


class StreamAnalyticsSummaryDefaultStreamPlaybackPolicy(BaseModel):
    """Per-playback-object access policy. Foghorn reads this in the USER_NEW
    trigger handler; the requiresAuth marker (on the playback object itself)
    gates whether the full policy is fetched at all."""

    type_: PlaybackPolicyType = Field(alias="type")
    allowed_origins: list[str] = Field(
        alias="allowedOrigins",
        description="Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied.",
    )
    "Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied."


class StreamAnalyticsSummaryDefaultStreamThumbnailAssets(BaseModel):
    """Chandler-served thumbnail asset URLs (poster, sprite, VTT cues). URL shape
    is `{chandlerBase}/assets/{assetKey}/poster.jpg`,
    `/assets/{assetKey}/sprite.jpg`, `/assets/{assetKey}/sprite.vtt` — Chandler
    serves the object key directly, no version resolution. assetKey is stream_id
    for live streams; clip_hash / dvr_hash / vod_hash (= artifact_hash) for artifacts."""

    poster_url: str = Field(alias="posterUrl")
    sprite_vtt_url: str = Field(alias="spriteVttUrl")
    sprite_jpg_url: str = Field(alias="spriteJpgUrl")
    asset_key: str = Field(alias="assetKey")


class StreamAnalyticsSummaryDefaultStreamRetentionOverrides(BaseModel):
    """Per-stream retention overrides for DVR and clips. VOD uploads aren't
    stream-bound, so they have no stream-level knob here."""

    stream_id: str = Field(alias="streamId")
    dvr_retention_days_override: Optional[int] = Field(
        alias="dvrRetentionDaysOverride",
        description="Null = no override (inherit tenant default). 0 = no auto-expire.",
    )
    "Null = no override (inherit tenant default). 0 = no auto-expire."
    clip_retention_days_override: Optional[int] = Field(
        alias="clipRetentionDaysOverride"
    )


class StreamAnalyticsSummaryDefaultTimeRange(BaseModel):
    """Time range returned in query results."""

    start: datetime = Field(description="Start of the time range.")
    "Start of the time range."
    end: datetime = Field(description="End of the time range.")
    "End of the time range."


class StreamAnalyticsSummaryDefaultRangeQuality(BaseModel):
    tier_2160_p_minutes: int = Field(alias="tier2160pMinutes")
    tier_1440_p_minutes: int = Field(alias="tier1440pMinutes")
    tier_1080_p_minutes: int = Field(alias="tier1080pMinutes")
    tier_720_p_minutes: int = Field(alias="tier720pMinutes")
    tier_480_p_minutes: int = Field(alias="tier480pMinutes")
    tier_sd_minutes: int = Field(alias="tierSdMinutes")
    codec_h_264_minutes: int = Field(alias="codecH264Minutes")
    codec_h_265_minutes: int = Field(alias="codecH265Minutes")
    codec_vp_9_minutes: int = Field(alias="codecVp9Minutes")
    codec_av_1_minutes: int = Field(alias="codecAv1Minutes")


class StreamConnectionHourlyDefault(BaseModel):
    id: str
    hour: datetime
    stream_id: str = Field(alias="streamId")
    stream: Optional["StreamConnectionHourlyDefaultStream"]
    total_bytes: float = Field(alias="totalBytes")
    unique_viewers: int = Field(alias="uniqueViewers")
    total_sessions: int = Field(alias="totalSessions")


class StreamConnectionHourlyDefaultStream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

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
        description="Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path.",
    )
    "Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path."
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
    pull_source: Optional["StreamConnectionHourlyDefaultStreamPullSource"] = Field(
        alias="pullSource",
        description="Pull-source config for pull streams; null for push streams.",
    )
    "Pull-source config for pull streams; null for push streams."
    managed_source: Optional["StreamConnectionHourlyDefaultStreamManagedSource"] = (
        Field(
            alias="managedSource",
            description="Safe source summary for managed streams; null for push and pull streams.",
        )
    )
    "Safe source summary for managed streams; null for push and pull streams."
    source_location: "StreamConnectionHourlyDefaultStreamSourceLocation" = Field(
        alias="sourceLocation",
        description="Where the stream's source may be ingested, derived from the stream's own ingest placement rules.",
    )
    "Where the stream's source may be ingested, derived from the stream's own ingest placement rules."
    created_at: datetime = Field(
        alias="createdAt", description="When this stream was created."
    )
    "When this stream was created."
    updated_at: datetime = Field(
        alias="updatedAt", description="When this stream was last modified."
    )
    "When this stream was last modified."
    metrics: Optional["StreamConnectionHourlyDefaultStreamMetrics"] = Field(
        description="Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    )
    "Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    push_targets: list["StreamConnectionHourlyDefaultStreamPushTargets"] = Field(
        alias="pushTargets",
        description="Configured multistream push targets for this stream.",
    )
    "Configured multistream push targets for this stream."
    playback_policy: Optional["StreamConnectionHourlyDefaultStreamPlaybackPolicy"] = (
        Field(
            alias="playbackPolicy",
            description="Playback access policy. null/PUBLIC = anyone with the playbackId can watch.",
        )
    )
    "Playback access policy. null/PUBLIC = anyone with the playbackId can watch."
    dvr_chapter_mode: Optional[DVRChapterMode] = Field(
        alias="dvrChapterMode",
        description="How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast.",
    )
    "How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast."
    dvr_chapter_interval_seconds: Optional[int] = Field(
        alias="dvrChapterIntervalSeconds",
        description="Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour).",
    )
    "Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour)."
    monitoring: MonitoringToggle = Field(
        description="Per-stream Skipper monitoring override (INHERIT follows tier)."
    )
    "Per-stream Skipper monitoring override (INHERIT follows tier)."
    thumbnail_assets: Optional["StreamConnectionHourlyDefaultStreamThumbnailAssets"] = (
        Field(
            alias="thumbnailAssets",
            description="Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles.",
        )
    )
    "Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles."
    retention_overrides: Optional[
        "StreamConnectionHourlyDefaultStreamRetentionOverrides"
    ] = Field(
        alias="retentionOverrides",
        description="Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here.",
    )
    "Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here."


class StreamConnectionHourlyDefaultStreamPullSource(BaseModel):
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


class StreamConnectionHourlyDefaultStreamManagedSource(BaseModel):
    """Safe summary of an operator-managed source. Literal paths, commands, and
    credentials are intentionally not exposed through the tenant API."""

    source_kind: str = Field(
        alias="sourceKind", description="Managed source form: file, playlist, or exec."
    )
    "Managed source form: file, playlist, or exec."
    always_on: bool = Field(
        alias="alwaysOn",
        description="Whether the source is kept active without waiting for a viewer.",
    )
    "Whether the source is kept active without waiting for a viewer."
    placement_count: int = Field(
        alias="placementCount",
        description="Number of source placements requested by the operator configuration.",
    )
    "Number of source placements requested by the operator configuration."


class StreamConnectionHourlyDefaultStreamSourceLocation(BaseModel):
    mode: SourceLocationMode
    avoid_node_ids: list[str] = Field(
        alias="avoidNodeIds", description="Empty unless mode is RESTRICTED."
    )
    "Empty unless mode is RESTRICTED."


class StreamConnectionHourlyDefaultStreamMetrics(BaseModel):
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
    node_id: Optional[str] = Field(
        alias="nodeId", description="ID of the edge node handling this stream."
    )
    "ID of the edge node handling this stream."
    track_count: Optional[int] = Field(
        alias="trackCount", description="Number of quality tracks available."
    )
    "Number of quality tracks available."
    total_inputs: Optional[int] = Field(
        alias="totalInputs", description="Total ingest connections to this stream."
    )
    "Total ingest connections to this stream."
    uploaded_bytes: float = Field(
        alias="uploadedBytes",
        description="Total bytes uploaded (ingested) for current session.",
    )
    "Total bytes uploaded (ingested) for current session."
    downloaded_bytes: float = Field(
        alias="downloadedBytes",
        description="Total bytes downloaded (egress) for current session.",
    )
    "Total bytes downloaded (egress) for current session."
    viewer_seconds: float = Field(
        alias="viewerSeconds", description="Total viewer-seconds accumulated."
    )
    "Total viewer-seconds accumulated."
    packets_sent: Optional[float] = Field(
        alias="packetsSent", description="Total packets sent to viewers."
    )
    "Total packets sent to viewers."
    packets_lost: Optional[float] = Field(
        alias="packetsLost", description="Total packets lost in transit."
    )
    "Total packets lost in transit."
    packets_retransmitted: Optional[float] = Field(
        alias="packetsRetransmitted", description="Total packets retransmitted."
    )
    "Total packets retransmitted."
    buffer_state: Optional[str] = Field(
        alias="bufferState",
        description="Buffer health state (HEALTHY, WARNING, CRITICAL).",
    )
    "Buffer health state (HEALTHY, WARNING, CRITICAL)."
    quality_tier: Optional[str] = Field(
        alias="qualityTier",
        description="Highest quality tier available (4K, 1080p, 720p, etc.).",
    )
    "Highest quality tier available (4K, 1080p, 720p, etc.)."
    primary_width: Optional[int] = Field(
        alias="primaryWidth", description="Primary video track width in pixels."
    )
    "Primary video track width in pixels."
    primary_height: Optional[int] = Field(
        alias="primaryHeight", description="Primary video track height in pixels."
    )
    "Primary video track height in pixels."
    primary_fps: Optional[float] = Field(
        alias="primaryFps", description="Primary video track framerate."
    )
    "Primary video track framerate."
    primary_codec: Optional[str] = Field(
        alias="primaryCodec",
        description="Primary video codec (H.264, H.265, VP9, AV1).",
    )
    "Primary video codec (H.264, H.265, VP9, AV1)."
    primary_bitrate: Optional[int] = Field(
        alias="primaryBitrate", description="Primary video bitrate in kbps."
    )
    "Primary video bitrate in kbps."
    has_issues: Optional[bool] = Field(
        alias="hasIssues", description="Whether the stream has active quality issues."
    )
    "Whether the stream has active quality issues."
    issues_description: Optional[str] = Field(
        alias="issuesDescription",
        description="Human-readable description of current issues.",
    )
    "Human-readable description of current issues."


class StreamConnectionHourlyDefaultStreamPushTargets(BaseModel):
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


class StreamConnectionHourlyDefaultStreamPlaybackPolicy(BaseModel):
    """Per-playback-object access policy. Foghorn reads this in the USER_NEW
    trigger handler; the requiresAuth marker (on the playback object itself)
    gates whether the full policy is fetched at all."""

    type_: PlaybackPolicyType = Field(alias="type")
    allowed_origins: list[str] = Field(
        alias="allowedOrigins",
        description="Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied.",
    )
    "Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied."


class StreamConnectionHourlyDefaultStreamThumbnailAssets(BaseModel):
    """Chandler-served thumbnail asset URLs (poster, sprite, VTT cues). URL shape
    is `{chandlerBase}/assets/{assetKey}/poster.jpg`,
    `/assets/{assetKey}/sprite.jpg`, `/assets/{assetKey}/sprite.vtt` — Chandler
    serves the object key directly, no version resolution. assetKey is stream_id
    for live streams; clip_hash / dvr_hash / vod_hash (= artifact_hash) for artifacts."""

    poster_url: str = Field(alias="posterUrl")
    sprite_vtt_url: str = Field(alias="spriteVttUrl")
    sprite_jpg_url: str = Field(alias="spriteJpgUrl")
    asset_key: str = Field(alias="assetKey")


class StreamConnectionHourlyDefaultStreamRetentionOverrides(BaseModel):
    """Per-stream retention overrides for DVR and clips. VOD uploads aren't
    stream-bound, so they have no stream-level knob here."""

    stream_id: str = Field(alias="streamId")
    dvr_retention_days_override: Optional[int] = Field(
        alias="dvrRetentionDaysOverride",
        description="Null = no override (inherit tenant default). 0 = no auto-expire.",
    )
    "Null = no override (inherit tenant default). 0 = no auto-expire."
    clip_retention_days_override: Optional[int] = Field(
        alias="clipRetentionDaysOverride"
    )


class StreamEventDefault(BaseModel):
    id: str
    event_id: str = Field(alias="eventId")
    stream_id: Optional[str] = Field(alias="streamId")
    stream: Optional["StreamEventDefaultStream"]
    node_id: Optional[str] = Field(alias="nodeId")
    type_: StreamEventType = Field(alias="type")
    status: Optional[StreamStatus]
    timestamp: datetime
    details: Optional[str]
    payload: Optional[Any]
    source: StreamEventSource
    buffer_state: Optional[str] = Field(alias="bufferState")
    has_issues: Optional[bool] = Field(alias="hasIssues")
    track_count: Optional[int] = Field(alias="trackCount")
    quality_tier: Optional[str] = Field(alias="qualityTier")
    primary_width: Optional[int] = Field(alias="primaryWidth")
    primary_height: Optional[int] = Field(alias="primaryHeight")
    primary_fps: Optional[float] = Field(alias="primaryFps")
    primary_codec: Optional[str] = Field(alias="primaryCodec")
    primary_bitrate: Optional[int] = Field(alias="primaryBitrate")
    downloaded_bytes: Optional[float] = Field(alias="downloadedBytes")
    uploaded_bytes: Optional[float] = Field(alias="uploadedBytes")
    total_viewers: Optional[int] = Field(alias="totalViewers")
    total_inputs: Optional[int] = Field(alias="totalInputs")
    total_outputs: Optional[int] = Field(alias="totalOutputs")
    viewer_seconds: Optional[float] = Field(alias="viewerSeconds")
    request_url: Optional[str] = Field(alias="requestUrl")
    protocol: Optional[str]
    latitude: Optional[float]
    longitude: Optional[float]
    location: Optional[str]
    country_code: Optional[str] = Field(alias="countryCode")
    city: Optional[str]
    source_region: str = Field(alias="sourceRegion")
    source_cluster_id: str = Field(alias="sourceClusterId")
    stream_origin_region: str = Field(alias="streamOriginRegion")
    stream_origin_cluster_id: str = Field(alias="streamOriginClusterId")
    schema_version: int = Field(alias="schemaVersion")


class StreamEventDefaultStream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

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
        description="Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path.",
    )
    "Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path."
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
    pull_source: Optional["StreamEventDefaultStreamPullSource"] = Field(
        alias="pullSource",
        description="Pull-source config for pull streams; null for push streams.",
    )
    "Pull-source config for pull streams; null for push streams."
    managed_source: Optional["StreamEventDefaultStreamManagedSource"] = Field(
        alias="managedSource",
        description="Safe source summary for managed streams; null for push and pull streams.",
    )
    "Safe source summary for managed streams; null for push and pull streams."
    source_location: "StreamEventDefaultStreamSourceLocation" = Field(
        alias="sourceLocation",
        description="Where the stream's source may be ingested, derived from the stream's own ingest placement rules.",
    )
    "Where the stream's source may be ingested, derived from the stream's own ingest placement rules."
    created_at: datetime = Field(
        alias="createdAt", description="When this stream was created."
    )
    "When this stream was created."
    updated_at: datetime = Field(
        alias="updatedAt", description="When this stream was last modified."
    )
    "When this stream was last modified."
    metrics: Optional["StreamEventDefaultStreamMetrics"] = Field(
        description="Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    )
    "Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    push_targets: list["StreamEventDefaultStreamPushTargets"] = Field(
        alias="pushTargets",
        description="Configured multistream push targets for this stream.",
    )
    "Configured multistream push targets for this stream."
    playback_policy: Optional["StreamEventDefaultStreamPlaybackPolicy"] = Field(
        alias="playbackPolicy",
        description="Playback access policy. null/PUBLIC = anyone with the playbackId can watch.",
    )
    "Playback access policy. null/PUBLIC = anyone with the playbackId can watch."
    dvr_chapter_mode: Optional[DVRChapterMode] = Field(
        alias="dvrChapterMode",
        description="How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast.",
    )
    "How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast."
    dvr_chapter_interval_seconds: Optional[int] = Field(
        alias="dvrChapterIntervalSeconds",
        description="Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour).",
    )
    "Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour)."
    monitoring: MonitoringToggle = Field(
        description="Per-stream Skipper monitoring override (INHERIT follows tier)."
    )
    "Per-stream Skipper monitoring override (INHERIT follows tier)."
    thumbnail_assets: Optional["StreamEventDefaultStreamThumbnailAssets"] = Field(
        alias="thumbnailAssets",
        description="Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles.",
    )
    "Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles."
    retention_overrides: Optional["StreamEventDefaultStreamRetentionOverrides"] = Field(
        alias="retentionOverrides",
        description="Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here.",
    )
    "Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here."


class StreamEventDefaultStreamPullSource(BaseModel):
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


class StreamEventDefaultStreamManagedSource(BaseModel):
    """Safe summary of an operator-managed source. Literal paths, commands, and
    credentials are intentionally not exposed through the tenant API."""

    source_kind: str = Field(
        alias="sourceKind", description="Managed source form: file, playlist, or exec."
    )
    "Managed source form: file, playlist, or exec."
    always_on: bool = Field(
        alias="alwaysOn",
        description="Whether the source is kept active without waiting for a viewer.",
    )
    "Whether the source is kept active without waiting for a viewer."
    placement_count: int = Field(
        alias="placementCount",
        description="Number of source placements requested by the operator configuration.",
    )
    "Number of source placements requested by the operator configuration."


class StreamEventDefaultStreamSourceLocation(BaseModel):
    mode: SourceLocationMode
    avoid_node_ids: list[str] = Field(
        alias="avoidNodeIds", description="Empty unless mode is RESTRICTED."
    )
    "Empty unless mode is RESTRICTED."


class StreamEventDefaultStreamMetrics(BaseModel):
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
    node_id: Optional[str] = Field(
        alias="nodeId", description="ID of the edge node handling this stream."
    )
    "ID of the edge node handling this stream."
    track_count: Optional[int] = Field(
        alias="trackCount", description="Number of quality tracks available."
    )
    "Number of quality tracks available."
    total_inputs: Optional[int] = Field(
        alias="totalInputs", description="Total ingest connections to this stream."
    )
    "Total ingest connections to this stream."
    uploaded_bytes: float = Field(
        alias="uploadedBytes",
        description="Total bytes uploaded (ingested) for current session.",
    )
    "Total bytes uploaded (ingested) for current session."
    downloaded_bytes: float = Field(
        alias="downloadedBytes",
        description="Total bytes downloaded (egress) for current session.",
    )
    "Total bytes downloaded (egress) for current session."
    viewer_seconds: float = Field(
        alias="viewerSeconds", description="Total viewer-seconds accumulated."
    )
    "Total viewer-seconds accumulated."
    packets_sent: Optional[float] = Field(
        alias="packetsSent", description="Total packets sent to viewers."
    )
    "Total packets sent to viewers."
    packets_lost: Optional[float] = Field(
        alias="packetsLost", description="Total packets lost in transit."
    )
    "Total packets lost in transit."
    packets_retransmitted: Optional[float] = Field(
        alias="packetsRetransmitted", description="Total packets retransmitted."
    )
    "Total packets retransmitted."
    buffer_state: Optional[str] = Field(
        alias="bufferState",
        description="Buffer health state (HEALTHY, WARNING, CRITICAL).",
    )
    "Buffer health state (HEALTHY, WARNING, CRITICAL)."
    quality_tier: Optional[str] = Field(
        alias="qualityTier",
        description="Highest quality tier available (4K, 1080p, 720p, etc.).",
    )
    "Highest quality tier available (4K, 1080p, 720p, etc.)."
    primary_width: Optional[int] = Field(
        alias="primaryWidth", description="Primary video track width in pixels."
    )
    "Primary video track width in pixels."
    primary_height: Optional[int] = Field(
        alias="primaryHeight", description="Primary video track height in pixels."
    )
    "Primary video track height in pixels."
    primary_fps: Optional[float] = Field(
        alias="primaryFps", description="Primary video track framerate."
    )
    "Primary video track framerate."
    primary_codec: Optional[str] = Field(
        alias="primaryCodec",
        description="Primary video codec (H.264, H.265, VP9, AV1).",
    )
    "Primary video codec (H.264, H.265, VP9, AV1)."
    primary_bitrate: Optional[int] = Field(
        alias="primaryBitrate", description="Primary video bitrate in kbps."
    )
    "Primary video bitrate in kbps."
    has_issues: Optional[bool] = Field(
        alias="hasIssues", description="Whether the stream has active quality issues."
    )
    "Whether the stream has active quality issues."
    issues_description: Optional[str] = Field(
        alias="issuesDescription",
        description="Human-readable description of current issues.",
    )
    "Human-readable description of current issues."


class StreamEventDefaultStreamPushTargets(BaseModel):
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


class StreamEventDefaultStreamPlaybackPolicy(BaseModel):
    """Per-playback-object access policy. Foghorn reads this in the USER_NEW
    trigger handler; the requiresAuth marker (on the playback object itself)
    gates whether the full policy is fetched at all."""

    type_: PlaybackPolicyType = Field(alias="type")
    allowed_origins: list[str] = Field(
        alias="allowedOrigins",
        description="Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied.",
    )
    "Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied."


class StreamEventDefaultStreamThumbnailAssets(BaseModel):
    """Chandler-served thumbnail asset URLs (poster, sprite, VTT cues). URL shape
    is `{chandlerBase}/assets/{assetKey}/poster.jpg`,
    `/assets/{assetKey}/sprite.jpg`, `/assets/{assetKey}/sprite.vtt` — Chandler
    serves the object key directly, no version resolution. assetKey is stream_id
    for live streams; clip_hash / dvr_hash / vod_hash (= artifact_hash) for artifacts."""

    poster_url: str = Field(alias="posterUrl")
    sprite_vtt_url: str = Field(alias="spriteVttUrl")
    sprite_jpg_url: str = Field(alias="spriteJpgUrl")
    asset_key: str = Field(alias="assetKey")


class StreamEventDefaultStreamRetentionOverrides(BaseModel):
    """Per-stream retention overrides for DVR and clips. VOD uploads aren't
    stream-bound, so they have no stream-level knob here."""

    stream_id: str = Field(alias="streamId")
    dvr_retention_days_override: Optional[int] = Field(
        alias="dvrRetentionDaysOverride",
        description="Null = no override (inherit tenant default). 0 = no auto-expire.",
    )
    "Null = no override (inherit tenant default). 0 = no auto-expire."
    clip_retention_days_override: Optional[int] = Field(
        alias="clipRetentionDaysOverride"
    )


class StreamEventInNodeDefault(BaseModel):
    id: str
    event_id: str = Field(alias="eventId")
    stream_event_stream_id: Optional[str] = Field(alias="streamEventStreamId")
    stream: Optional["StreamEventInNodeDefaultStream"]
    stream_event_node_id: Optional[str] = Field(alias="streamEventNodeId")
    type_: StreamEventType = Field(alias="type")
    stream_event_status: Optional[StreamStatus] = Field(alias="streamEventStatus")
    timestamp: datetime
    details: Optional[str]
    payload: Optional[Any]
    source: StreamEventSource
    stream_event_buffer_state: Optional[str] = Field(alias="streamEventBufferState")
    has_issues: Optional[bool] = Field(alias="hasIssues")
    track_count: Optional[int] = Field(alias="trackCount")
    quality_tier: Optional[str] = Field(alias="qualityTier")
    primary_width: Optional[int] = Field(alias="primaryWidth")
    primary_height: Optional[int] = Field(alias="primaryHeight")
    primary_fps: Optional[float] = Field(alias="primaryFps")
    primary_codec: Optional[str] = Field(alias="primaryCodec")
    primary_bitrate: Optional[int] = Field(alias="primaryBitrate")
    downloaded_bytes: Optional[float] = Field(alias="downloadedBytes")
    uploaded_bytes: Optional[float] = Field(alias="uploadedBytes")
    total_viewers: Optional[int] = Field(alias="totalViewers")
    total_inputs: Optional[int] = Field(alias="totalInputs")
    total_outputs: Optional[int] = Field(alias="totalOutputs")
    viewer_seconds: Optional[float] = Field(alias="viewerSeconds")
    request_url: Optional[str] = Field(alias="requestUrl")
    protocol: Optional[str]
    latitude: Optional[float]
    longitude: Optional[float]
    location: Optional[str]
    country_code: Optional[str] = Field(alias="countryCode")
    city: Optional[str]
    source_region: str = Field(alias="sourceRegion")
    source_cluster_id: str = Field(alias="sourceClusterId")
    stream_origin_region: str = Field(alias="streamOriginRegion")
    stream_origin_cluster_id: str = Field(alias="streamOriginClusterId")
    schema_version: int = Field(alias="schemaVersion")


class StreamEventInNodeDefaultStream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

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
        description="Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path.",
    )
    "Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path."
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
    pull_source: Optional["StreamEventInNodeDefaultStreamPullSource"] = Field(
        alias="pullSource",
        description="Pull-source config for pull streams; null for push streams.",
    )
    "Pull-source config for pull streams; null for push streams."
    managed_source: Optional["StreamEventInNodeDefaultStreamManagedSource"] = Field(
        alias="managedSource",
        description="Safe source summary for managed streams; null for push and pull streams.",
    )
    "Safe source summary for managed streams; null for push and pull streams."
    source_location: "StreamEventInNodeDefaultStreamSourceLocation" = Field(
        alias="sourceLocation",
        description="Where the stream's source may be ingested, derived from the stream's own ingest placement rules.",
    )
    "Where the stream's source may be ingested, derived from the stream's own ingest placement rules."
    created_at: datetime = Field(
        alias="createdAt", description="When this stream was created."
    )
    "When this stream was created."
    updated_at: datetime = Field(
        alias="updatedAt", description="When this stream was last modified."
    )
    "When this stream was last modified."
    metrics: Optional["StreamEventInNodeDefaultStreamMetrics"] = Field(
        description="Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    )
    "Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    push_targets: list["StreamEventInNodeDefaultStreamPushTargets"] = Field(
        alias="pushTargets",
        description="Configured multistream push targets for this stream.",
    )
    "Configured multistream push targets for this stream."
    playback_policy: Optional["StreamEventInNodeDefaultStreamPlaybackPolicy"] = Field(
        alias="playbackPolicy",
        description="Playback access policy. null/PUBLIC = anyone with the playbackId can watch.",
    )
    "Playback access policy. null/PUBLIC = anyone with the playbackId can watch."
    dvr_chapter_mode: Optional[DVRChapterMode] = Field(
        alias="dvrChapterMode",
        description="How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast.",
    )
    "How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast."
    dvr_chapter_interval_seconds: Optional[int] = Field(
        alias="dvrChapterIntervalSeconds",
        description="Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour).",
    )
    "Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour)."
    monitoring: MonitoringToggle = Field(
        description="Per-stream Skipper monitoring override (INHERIT follows tier)."
    )
    "Per-stream Skipper monitoring override (INHERIT follows tier)."
    thumbnail_assets: Optional["StreamEventInNodeDefaultStreamThumbnailAssets"] = Field(
        alias="thumbnailAssets",
        description="Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles.",
    )
    "Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles."
    retention_overrides: Optional[
        "StreamEventInNodeDefaultStreamRetentionOverrides"
    ] = Field(
        alias="retentionOverrides",
        description="Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here.",
    )
    "Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here."


class StreamEventInNodeDefaultStreamPullSource(BaseModel):
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


class StreamEventInNodeDefaultStreamManagedSource(BaseModel):
    """Safe summary of an operator-managed source. Literal paths, commands, and
    credentials are intentionally not exposed through the tenant API."""

    source_kind: str = Field(
        alias="sourceKind", description="Managed source form: file, playlist, or exec."
    )
    "Managed source form: file, playlist, or exec."
    always_on: bool = Field(
        alias="alwaysOn",
        description="Whether the source is kept active without waiting for a viewer.",
    )
    "Whether the source is kept active without waiting for a viewer."
    placement_count: int = Field(
        alias="placementCount",
        description="Number of source placements requested by the operator configuration.",
    )
    "Number of source placements requested by the operator configuration."


class StreamEventInNodeDefaultStreamSourceLocation(BaseModel):
    mode: SourceLocationMode
    avoid_node_ids: list[str] = Field(
        alias="avoidNodeIds", description="Empty unless mode is RESTRICTED."
    )
    "Empty unless mode is RESTRICTED."


class StreamEventInNodeDefaultStreamMetrics(BaseModel):
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
    node_id: Optional[str] = Field(
        alias="nodeId", description="ID of the edge node handling this stream."
    )
    "ID of the edge node handling this stream."
    track_count: Optional[int] = Field(
        alias="trackCount", description="Number of quality tracks available."
    )
    "Number of quality tracks available."
    total_inputs: Optional[int] = Field(
        alias="totalInputs", description="Total ingest connections to this stream."
    )
    "Total ingest connections to this stream."
    uploaded_bytes: float = Field(
        alias="uploadedBytes",
        description="Total bytes uploaded (ingested) for current session.",
    )
    "Total bytes uploaded (ingested) for current session."
    downloaded_bytes: float = Field(
        alias="downloadedBytes",
        description="Total bytes downloaded (egress) for current session.",
    )
    "Total bytes downloaded (egress) for current session."
    viewer_seconds: float = Field(
        alias="viewerSeconds", description="Total viewer-seconds accumulated."
    )
    "Total viewer-seconds accumulated."
    packets_sent: Optional[float] = Field(
        alias="packetsSent", description="Total packets sent to viewers."
    )
    "Total packets sent to viewers."
    packets_lost: Optional[float] = Field(
        alias="packetsLost", description="Total packets lost in transit."
    )
    "Total packets lost in transit."
    packets_retransmitted: Optional[float] = Field(
        alias="packetsRetransmitted", description="Total packets retransmitted."
    )
    "Total packets retransmitted."
    buffer_state: Optional[str] = Field(
        alias="bufferState",
        description="Buffer health state (HEALTHY, WARNING, CRITICAL).",
    )
    "Buffer health state (HEALTHY, WARNING, CRITICAL)."
    quality_tier: Optional[str] = Field(
        alias="qualityTier",
        description="Highest quality tier available (4K, 1080p, 720p, etc.).",
    )
    "Highest quality tier available (4K, 1080p, 720p, etc.)."
    primary_width: Optional[int] = Field(
        alias="primaryWidth", description="Primary video track width in pixels."
    )
    "Primary video track width in pixels."
    primary_height: Optional[int] = Field(
        alias="primaryHeight", description="Primary video track height in pixels."
    )
    "Primary video track height in pixels."
    primary_fps: Optional[float] = Field(
        alias="primaryFps", description="Primary video track framerate."
    )
    "Primary video track framerate."
    primary_codec: Optional[str] = Field(
        alias="primaryCodec",
        description="Primary video codec (H.264, H.265, VP9, AV1).",
    )
    "Primary video codec (H.264, H.265, VP9, AV1)."
    primary_bitrate: Optional[int] = Field(
        alias="primaryBitrate", description="Primary video bitrate in kbps."
    )
    "Primary video bitrate in kbps."
    has_issues: Optional[bool] = Field(
        alias="hasIssues", description="Whether the stream has active quality issues."
    )
    "Whether the stream has active quality issues."
    issues_description: Optional[str] = Field(
        alias="issuesDescription",
        description="Human-readable description of current issues.",
    )
    "Human-readable description of current issues."


class StreamEventInNodeDefaultStreamPushTargets(BaseModel):
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


class StreamEventInNodeDefaultStreamPlaybackPolicy(BaseModel):
    """Per-playback-object access policy. Foghorn reads this in the USER_NEW
    trigger handler; the requiresAuth marker (on the playback object itself)
    gates whether the full policy is fetched at all."""

    type_: PlaybackPolicyType = Field(alias="type")
    allowed_origins: list[str] = Field(
        alias="allowedOrigins",
        description="Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied.",
    )
    "Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied."


class StreamEventInNodeDefaultStreamThumbnailAssets(BaseModel):
    """Chandler-served thumbnail asset URLs (poster, sprite, VTT cues). URL shape
    is `{chandlerBase}/assets/{assetKey}/poster.jpg`,
    `/assets/{assetKey}/sprite.jpg`, `/assets/{assetKey}/sprite.vtt` — Chandler
    serves the object key directly, no version resolution. assetKey is stream_id
    for live streams; clip_hash / dvr_hash / vod_hash (= artifact_hash) for artifacts."""

    poster_url: str = Field(alias="posterUrl")
    sprite_vtt_url: str = Field(alias="spriteVttUrl")
    sprite_jpg_url: str = Field(alias="spriteJpgUrl")
    asset_key: str = Field(alias="assetKey")


class StreamEventInNodeDefaultStreamRetentionOverrides(BaseModel):
    """Per-stream retention overrides for DVR and clips. VOD uploads aren't
    stream-bound, so they have no stream-level knob here."""

    stream_id: str = Field(alias="streamId")
    dvr_retention_days_override: Optional[int] = Field(
        alias="dvrRetentionDaysOverride",
        description="Null = no override (inherit tenant default). 0 = no auto-expire.",
    )
    "Null = no override (inherit tenant default). 0 = no auto-expire."
    clip_retention_days_override: Optional[int] = Field(
        alias="clipRetentionDaysOverride"
    )


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
        description="How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast.",
    )
    "How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast."
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


class StreamHealth5mDefault(BaseModel):
    id: str
    timestamp: datetime
    node_id: Optional[str] = Field(alias="nodeId")
    rebuffer_count: int = Field(alias="rebufferCount")
    issue_count: int = Field(alias="issueCount")
    sample_issues: Optional[str] = Field(alias="sampleIssues")
    avg_bitrate: int = Field(alias="avgBitrate")
    avg_fps: float = Field(alias="avgFps")
    avg_buffer_health: float = Field(alias="avgBufferHealth")
    avg_frame_jitter_ms: Optional[float] = Field(alias="avgFrameJitterMs")
    max_frame_jitter_ms: Optional[float] = Field(alias="maxFrameJitterMs")
    buffer_dry_count: int = Field(alias="bufferDryCount")
    quality_tier: Optional[str] = Field(alias="qualityTier")


class StreamHealth5mInNodeDefault(BaseModel):
    id: str
    timestamp: datetime
    stream_health_5_m_node_id: Optional[str] = Field(alias="streamHealth5mNodeId")
    rebuffer_count: int = Field(alias="rebufferCount")
    issue_count: int = Field(alias="issueCount")
    sample_issues: Optional[str] = Field(alias="sampleIssues")
    avg_bitrate: int = Field(alias="avgBitrate")
    avg_fps: float = Field(alias="avgFps")
    avg_buffer_health: float = Field(alias="avgBufferHealth")
    avg_frame_jitter_ms: Optional[float] = Field(alias="avgFrameJitterMs")
    max_frame_jitter_ms: Optional[float] = Field(alias="maxFrameJitterMs")
    buffer_dry_count: int = Field(alias="bufferDryCount")
    quality_tier: Optional[str] = Field(alias="qualityTier")


class StreamHealthMetricDefault(BaseModel):
    id: str
    timestamp: datetime
    stream_id: str = Field(alias="streamId")
    stream: Optional["StreamHealthMetricDefaultStream"]
    node_id: str = Field(alias="nodeId")
    issues_description: Optional[str] = Field(alias="issuesDescription")
    has_issues: bool = Field(alias="hasIssues")
    bitrate: Optional[int]
    fps: Optional[float]
    width: Optional[int]
    height: Optional[int]
    codec: Optional[str]
    quality_tier: Optional[str] = Field(alias="qualityTier")
    gop_size: Optional[int] = Field(alias="gopSize")
    frame_ms_max: Optional[float] = Field(alias="frameMsMax")
    frame_ms_min: Optional[float] = Field(alias="frameMsMin")
    frames_max: Optional[int] = Field(alias="framesMax")
    frames_min: Optional[int] = Field(alias="framesMin")
    keyframe_ms_max: Optional[float] = Field(alias="keyframeMsMax")
    keyframe_ms_min: Optional[float] = Field(alias="keyframeMsMin")
    frame_jitter_ms: Optional[float] = Field(alias="frameJitterMs")
    track_count: Optional[int] = Field(alias="trackCount")
    buffer_state: BufferState = Field(alias="bufferState")
    buffer_health: Optional[float] = Field(alias="bufferHealth")
    buffer_size: Optional[int] = Field(alias="bufferSize")
    audio_channels: Optional[int] = Field(alias="audioChannels")
    audio_sample_rate: Optional[int] = Field(alias="audioSampleRate")
    audio_codec: Optional[str] = Field(alias="audioCodec")
    audio_bitrate: Optional[int] = Field(alias="audioBitrate")
    track_metadata: Optional[Any] = Field(alias="trackMetadata")


class StreamHealthMetricDefaultStream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

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
        description="Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path.",
    )
    "Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path."
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
    pull_source: Optional["StreamHealthMetricDefaultStreamPullSource"] = Field(
        alias="pullSource",
        description="Pull-source config for pull streams; null for push streams.",
    )
    "Pull-source config for pull streams; null for push streams."
    managed_source: Optional["StreamHealthMetricDefaultStreamManagedSource"] = Field(
        alias="managedSource",
        description="Safe source summary for managed streams; null for push and pull streams.",
    )
    "Safe source summary for managed streams; null for push and pull streams."
    source_location: "StreamHealthMetricDefaultStreamSourceLocation" = Field(
        alias="sourceLocation",
        description="Where the stream's source may be ingested, derived from the stream's own ingest placement rules.",
    )
    "Where the stream's source may be ingested, derived from the stream's own ingest placement rules."
    created_at: datetime = Field(
        alias="createdAt", description="When this stream was created."
    )
    "When this stream was created."
    updated_at: datetime = Field(
        alias="updatedAt", description="When this stream was last modified."
    )
    "When this stream was last modified."
    metrics: Optional["StreamHealthMetricDefaultStreamMetrics"] = Field(
        description="Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    )
    "Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    push_targets: list["StreamHealthMetricDefaultStreamPushTargets"] = Field(
        alias="pushTargets",
        description="Configured multistream push targets for this stream.",
    )
    "Configured multistream push targets for this stream."
    playback_policy: Optional["StreamHealthMetricDefaultStreamPlaybackPolicy"] = Field(
        alias="playbackPolicy",
        description="Playback access policy. null/PUBLIC = anyone with the playbackId can watch.",
    )
    "Playback access policy. null/PUBLIC = anyone with the playbackId can watch."
    dvr_chapter_mode: Optional[DVRChapterMode] = Field(
        alias="dvrChapterMode",
        description="How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast.",
    )
    "How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast."
    dvr_chapter_interval_seconds: Optional[int] = Field(
        alias="dvrChapterIntervalSeconds",
        description="Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour).",
    )
    "Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour)."
    monitoring: MonitoringToggle = Field(
        description="Per-stream Skipper monitoring override (INHERIT follows tier)."
    )
    "Per-stream Skipper monitoring override (INHERIT follows tier)."
    thumbnail_assets: Optional["StreamHealthMetricDefaultStreamThumbnailAssets"] = (
        Field(
            alias="thumbnailAssets",
            description="Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles.",
        )
    )
    "Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles."
    retention_overrides: Optional[
        "StreamHealthMetricDefaultStreamRetentionOverrides"
    ] = Field(
        alias="retentionOverrides",
        description="Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here.",
    )
    "Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here."


class StreamHealthMetricDefaultStreamPullSource(BaseModel):
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


class StreamHealthMetricDefaultStreamManagedSource(BaseModel):
    """Safe summary of an operator-managed source. Literal paths, commands, and
    credentials are intentionally not exposed through the tenant API."""

    source_kind: str = Field(
        alias="sourceKind", description="Managed source form: file, playlist, or exec."
    )
    "Managed source form: file, playlist, or exec."
    always_on: bool = Field(
        alias="alwaysOn",
        description="Whether the source is kept active without waiting for a viewer.",
    )
    "Whether the source is kept active without waiting for a viewer."
    placement_count: int = Field(
        alias="placementCount",
        description="Number of source placements requested by the operator configuration.",
    )
    "Number of source placements requested by the operator configuration."


class StreamHealthMetricDefaultStreamSourceLocation(BaseModel):
    mode: SourceLocationMode
    avoid_node_ids: list[str] = Field(
        alias="avoidNodeIds", description="Empty unless mode is RESTRICTED."
    )
    "Empty unless mode is RESTRICTED."


class StreamHealthMetricDefaultStreamMetrics(BaseModel):
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
    node_id: Optional[str] = Field(
        alias="nodeId", description="ID of the edge node handling this stream."
    )
    "ID of the edge node handling this stream."
    track_count: Optional[int] = Field(
        alias="trackCount", description="Number of quality tracks available."
    )
    "Number of quality tracks available."
    total_inputs: Optional[int] = Field(
        alias="totalInputs", description="Total ingest connections to this stream."
    )
    "Total ingest connections to this stream."
    uploaded_bytes: float = Field(
        alias="uploadedBytes",
        description="Total bytes uploaded (ingested) for current session.",
    )
    "Total bytes uploaded (ingested) for current session."
    downloaded_bytes: float = Field(
        alias="downloadedBytes",
        description="Total bytes downloaded (egress) for current session.",
    )
    "Total bytes downloaded (egress) for current session."
    viewer_seconds: float = Field(
        alias="viewerSeconds", description="Total viewer-seconds accumulated."
    )
    "Total viewer-seconds accumulated."
    packets_sent: Optional[float] = Field(
        alias="packetsSent", description="Total packets sent to viewers."
    )
    "Total packets sent to viewers."
    packets_lost: Optional[float] = Field(
        alias="packetsLost", description="Total packets lost in transit."
    )
    "Total packets lost in transit."
    packets_retransmitted: Optional[float] = Field(
        alias="packetsRetransmitted", description="Total packets retransmitted."
    )
    "Total packets retransmitted."
    buffer_state: Optional[str] = Field(
        alias="bufferState",
        description="Buffer health state (HEALTHY, WARNING, CRITICAL).",
    )
    "Buffer health state (HEALTHY, WARNING, CRITICAL)."
    quality_tier: Optional[str] = Field(
        alias="qualityTier",
        description="Highest quality tier available (4K, 1080p, 720p, etc.).",
    )
    "Highest quality tier available (4K, 1080p, 720p, etc.)."
    primary_width: Optional[int] = Field(
        alias="primaryWidth", description="Primary video track width in pixels."
    )
    "Primary video track width in pixels."
    primary_height: Optional[int] = Field(
        alias="primaryHeight", description="Primary video track height in pixels."
    )
    "Primary video track height in pixels."
    primary_fps: Optional[float] = Field(
        alias="primaryFps", description="Primary video track framerate."
    )
    "Primary video track framerate."
    primary_codec: Optional[str] = Field(
        alias="primaryCodec",
        description="Primary video codec (H.264, H.265, VP9, AV1).",
    )
    "Primary video codec (H.264, H.265, VP9, AV1)."
    primary_bitrate: Optional[int] = Field(
        alias="primaryBitrate", description="Primary video bitrate in kbps."
    )
    "Primary video bitrate in kbps."
    has_issues: Optional[bool] = Field(
        alias="hasIssues", description="Whether the stream has active quality issues."
    )
    "Whether the stream has active quality issues."
    issues_description: Optional[str] = Field(
        alias="issuesDescription",
        description="Human-readable description of current issues.",
    )
    "Human-readable description of current issues."


class StreamHealthMetricDefaultStreamPushTargets(BaseModel):
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


class StreamHealthMetricDefaultStreamPlaybackPolicy(BaseModel):
    """Per-playback-object access policy. Foghorn reads this in the USER_NEW
    trigger handler; the requiresAuth marker (on the playback object itself)
    gates whether the full policy is fetched at all."""

    type_: PlaybackPolicyType = Field(alias="type")
    allowed_origins: list[str] = Field(
        alias="allowedOrigins",
        description="Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied.",
    )
    "Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied."


class StreamHealthMetricDefaultStreamThumbnailAssets(BaseModel):
    """Chandler-served thumbnail asset URLs (poster, sprite, VTT cues). URL shape
    is `{chandlerBase}/assets/{assetKey}/poster.jpg`,
    `/assets/{assetKey}/sprite.jpg`, `/assets/{assetKey}/sprite.vtt` — Chandler
    serves the object key directly, no version resolution. assetKey is stream_id
    for live streams; clip_hash / dvr_hash / vod_hash (= artifact_hash) for artifacts."""

    poster_url: str = Field(alias="posterUrl")
    sprite_vtt_url: str = Field(alias="spriteVttUrl")
    sprite_jpg_url: str = Field(alias="spriteJpgUrl")
    asset_key: str = Field(alias="assetKey")


class StreamHealthMetricDefaultStreamRetentionOverrides(BaseModel):
    """Per-stream retention overrides for DVR and clips. VOD uploads aren't
    stream-bound, so they have no stream-level knob here."""

    stream_id: str = Field(alias="streamId")
    dvr_retention_days_override: Optional[int] = Field(
        alias="dvrRetentionDaysOverride",
        description="Null = no override (inherit tenant default). 0 = no auto-expire.",
    )
    "Null = no override (inherit tenant default). 0 = no auto-expire."
    clip_retention_days_override: Optional[int] = Field(
        alias="clipRetentionDaysOverride"
    )


class StreamHealthMetricInNodeDefault(BaseModel):
    id: str
    timestamp: datetime
    stream_id: str = Field(alias="streamId")
    stream: Optional["StreamHealthMetricInNodeDefaultStream"]
    node_id: str = Field(alias="nodeId")
    issues_description: Optional[str] = Field(alias="issuesDescription")
    stream_health_metric_has_issues: bool = Field(alias="streamHealthMetricHasIssues")
    bitrate: Optional[int]
    fps: Optional[float]
    width: Optional[int]
    height: Optional[int]
    codec: Optional[str]
    quality_tier: Optional[str] = Field(alias="qualityTier")
    gop_size: Optional[int] = Field(alias="gopSize")
    frame_ms_max: Optional[float] = Field(alias="frameMsMax")
    frame_ms_min: Optional[float] = Field(alias="frameMsMin")
    frames_max: Optional[int] = Field(alias="framesMax")
    frames_min: Optional[int] = Field(alias="framesMin")
    keyframe_ms_max: Optional[float] = Field(alias="keyframeMsMax")
    keyframe_ms_min: Optional[float] = Field(alias="keyframeMsMin")
    frame_jitter_ms: Optional[float] = Field(alias="frameJitterMs")
    track_count: Optional[int] = Field(alias="trackCount")
    buffer_state: BufferState = Field(alias="bufferState")
    buffer_health: Optional[float] = Field(alias="bufferHealth")
    buffer_size: Optional[int] = Field(alias="bufferSize")
    audio_channels: Optional[int] = Field(alias="audioChannels")
    audio_sample_rate: Optional[int] = Field(alias="audioSampleRate")
    audio_codec: Optional[str] = Field(alias="audioCodec")
    audio_bitrate: Optional[int] = Field(alias="audioBitrate")
    track_metadata: Optional[Any] = Field(alias="trackMetadata")


class StreamHealthMetricInNodeDefaultStream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

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
        description="Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path.",
    )
    "Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path."
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
    pull_source: Optional["StreamHealthMetricInNodeDefaultStreamPullSource"] = Field(
        alias="pullSource",
        description="Pull-source config for pull streams; null for push streams.",
    )
    "Pull-source config for pull streams; null for push streams."
    managed_source: Optional["StreamHealthMetricInNodeDefaultStreamManagedSource"] = (
        Field(
            alias="managedSource",
            description="Safe source summary for managed streams; null for push and pull streams.",
        )
    )
    "Safe source summary for managed streams; null for push and pull streams."
    source_location: "StreamHealthMetricInNodeDefaultStreamSourceLocation" = Field(
        alias="sourceLocation",
        description="Where the stream's source may be ingested, derived from the stream's own ingest placement rules.",
    )
    "Where the stream's source may be ingested, derived from the stream's own ingest placement rules."
    created_at: datetime = Field(
        alias="createdAt", description="When this stream was created."
    )
    "When this stream was created."
    updated_at: datetime = Field(
        alias="updatedAt", description="When this stream was last modified."
    )
    "When this stream was last modified."
    metrics: Optional["StreamHealthMetricInNodeDefaultStreamMetrics"] = Field(
        description="Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    )
    "Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    push_targets: list["StreamHealthMetricInNodeDefaultStreamPushTargets"] = Field(
        alias="pushTargets",
        description="Configured multistream push targets for this stream.",
    )
    "Configured multistream push targets for this stream."
    playback_policy: Optional["StreamHealthMetricInNodeDefaultStreamPlaybackPolicy"] = (
        Field(
            alias="playbackPolicy",
            description="Playback access policy. null/PUBLIC = anyone with the playbackId can watch.",
        )
    )
    "Playback access policy. null/PUBLIC = anyone with the playbackId can watch."
    dvr_chapter_mode: Optional[DVRChapterMode] = Field(
        alias="dvrChapterMode",
        description="How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast.",
    )
    "How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast."
    dvr_chapter_interval_seconds: Optional[int] = Field(
        alias="dvrChapterIntervalSeconds",
        description="Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour).",
    )
    "Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour)."
    monitoring: MonitoringToggle = Field(
        description="Per-stream Skipper monitoring override (INHERIT follows tier)."
    )
    "Per-stream Skipper monitoring override (INHERIT follows tier)."
    thumbnail_assets: Optional[
        "StreamHealthMetricInNodeDefaultStreamThumbnailAssets"
    ] = Field(
        alias="thumbnailAssets",
        description="Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles.",
    )
    "Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles."
    retention_overrides: Optional[
        "StreamHealthMetricInNodeDefaultStreamRetentionOverrides"
    ] = Field(
        alias="retentionOverrides",
        description="Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here.",
    )
    "Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here."


class StreamHealthMetricInNodeDefaultStreamPullSource(BaseModel):
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


class StreamHealthMetricInNodeDefaultStreamManagedSource(BaseModel):
    """Safe summary of an operator-managed source. Literal paths, commands, and
    credentials are intentionally not exposed through the tenant API."""

    source_kind: str = Field(
        alias="sourceKind", description="Managed source form: file, playlist, or exec."
    )
    "Managed source form: file, playlist, or exec."
    always_on: bool = Field(
        alias="alwaysOn",
        description="Whether the source is kept active without waiting for a viewer.",
    )
    "Whether the source is kept active without waiting for a viewer."
    placement_count: int = Field(
        alias="placementCount",
        description="Number of source placements requested by the operator configuration.",
    )
    "Number of source placements requested by the operator configuration."


class StreamHealthMetricInNodeDefaultStreamSourceLocation(BaseModel):
    mode: SourceLocationMode
    avoid_node_ids: list[str] = Field(
        alias="avoidNodeIds", description="Empty unless mode is RESTRICTED."
    )
    "Empty unless mode is RESTRICTED."


class StreamHealthMetricInNodeDefaultStreamMetrics(BaseModel):
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
    node_id: Optional[str] = Field(
        alias="nodeId", description="ID of the edge node handling this stream."
    )
    "ID of the edge node handling this stream."
    track_count: Optional[int] = Field(
        alias="trackCount", description="Number of quality tracks available."
    )
    "Number of quality tracks available."
    total_inputs: Optional[int] = Field(
        alias="totalInputs", description="Total ingest connections to this stream."
    )
    "Total ingest connections to this stream."
    uploaded_bytes: float = Field(
        alias="uploadedBytes",
        description="Total bytes uploaded (ingested) for current session.",
    )
    "Total bytes uploaded (ingested) for current session."
    downloaded_bytes: float = Field(
        alias="downloadedBytes",
        description="Total bytes downloaded (egress) for current session.",
    )
    "Total bytes downloaded (egress) for current session."
    viewer_seconds: float = Field(
        alias="viewerSeconds", description="Total viewer-seconds accumulated."
    )
    "Total viewer-seconds accumulated."
    packets_sent: Optional[float] = Field(
        alias="packetsSent", description="Total packets sent to viewers."
    )
    "Total packets sent to viewers."
    packets_lost: Optional[float] = Field(
        alias="packetsLost", description="Total packets lost in transit."
    )
    "Total packets lost in transit."
    packets_retransmitted: Optional[float] = Field(
        alias="packetsRetransmitted", description="Total packets retransmitted."
    )
    "Total packets retransmitted."
    buffer_state: Optional[str] = Field(
        alias="bufferState",
        description="Buffer health state (HEALTHY, WARNING, CRITICAL).",
    )
    "Buffer health state (HEALTHY, WARNING, CRITICAL)."
    quality_tier: Optional[str] = Field(
        alias="qualityTier",
        description="Highest quality tier available (4K, 1080p, 720p, etc.).",
    )
    "Highest quality tier available (4K, 1080p, 720p, etc.)."
    primary_width: Optional[int] = Field(
        alias="primaryWidth", description="Primary video track width in pixels."
    )
    "Primary video track width in pixels."
    primary_height: Optional[int] = Field(
        alias="primaryHeight", description="Primary video track height in pixels."
    )
    "Primary video track height in pixels."
    primary_fps: Optional[float] = Field(
        alias="primaryFps", description="Primary video track framerate."
    )
    "Primary video track framerate."
    primary_codec: Optional[str] = Field(
        alias="primaryCodec",
        description="Primary video codec (H.264, H.265, VP9, AV1).",
    )
    "Primary video codec (H.264, H.265, VP9, AV1)."
    primary_bitrate: Optional[int] = Field(
        alias="primaryBitrate", description="Primary video bitrate in kbps."
    )
    "Primary video bitrate in kbps."
    has_issues: Optional[bool] = Field(
        alias="hasIssues", description="Whether the stream has active quality issues."
    )
    "Whether the stream has active quality issues."
    issues_description: Optional[str] = Field(
        alias="issuesDescription",
        description="Human-readable description of current issues.",
    )
    "Human-readable description of current issues."


class StreamHealthMetricInNodeDefaultStreamPushTargets(BaseModel):
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


class StreamHealthMetricInNodeDefaultStreamPlaybackPolicy(BaseModel):
    """Per-playback-object access policy. Foghorn reads this in the USER_NEW
    trigger handler; the requiresAuth marker (on the playback object itself)
    gates whether the full policy is fetched at all."""

    type_: PlaybackPolicyType = Field(alias="type")
    allowed_origins: list[str] = Field(
        alias="allowedOrigins",
        description="Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied.",
    )
    "Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied."


class StreamHealthMetricInNodeDefaultStreamThumbnailAssets(BaseModel):
    """Chandler-served thumbnail asset URLs (poster, sprite, VTT cues). URL shape
    is `{chandlerBase}/assets/{assetKey}/poster.jpg`,
    `/assets/{assetKey}/sprite.jpg`, `/assets/{assetKey}/sprite.vtt` — Chandler
    serves the object key directly, no version resolution. assetKey is stream_id
    for live streams; clip_hash / dvr_hash / vod_hash (= artifact_hash) for artifacts."""

    poster_url: str = Field(alias="posterUrl")
    sprite_vtt_url: str = Field(alias="spriteVttUrl")
    sprite_jpg_url: str = Field(alias="spriteJpgUrl")
    asset_key: str = Field(alias="assetKey")


class StreamHealthMetricInNodeDefaultStreamRetentionOverrides(BaseModel):
    """Per-stream retention overrides for DVR and clips. VOD uploads aren't
    stream-bound, so they have no stream-level knob here."""

    stream_id: str = Field(alias="streamId")
    dvr_retention_days_override: Optional[int] = Field(
        alias="dvrRetentionDaysOverride",
        description="Null = no override (inherit tenant default). 0 = no auto-expire.",
    )
    "Null = no override (inherit tenant default). 0 = no auto-expire."
    clip_retention_days_override: Optional[int] = Field(
        alias="clipRetentionDaysOverride"
    )


class StreamHealthSummaryDefault(BaseModel):
    """Pre-aggregated stream health summary from stream_health_5m."""

    avg_bitrate: float = Field(alias="avgBitrate")
    avg_fps: float = Field(alias="avgFps")
    avg_buffer_health: float = Field(alias="avgBufferHealth")
    total_rebuffer_count: int = Field(alias="totalRebufferCount")
    total_issue_count: int = Field(alias="totalIssueCount")
    sample_count: int = Field(alias="sampleCount")
    has_active_issues: bool = Field(alias="hasActiveIssues")
    current_quality_tier: Optional[str] = Field(alias="currentQualityTier")


class StreamInNodeDefault(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

    id: str = Field(description="Global unique identifier for Relay compatibility.")
    "Global unique identifier for Relay compatibility."
    stream_stream_id: str = Field(
        alias="streamStreamId",
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
        description="Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path.",
    )
    "Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path."
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
    pull_source: Optional["StreamInNodeDefaultPullSource"] = Field(
        alias="pullSource",
        description="Pull-source config for pull streams; null for push streams.",
    )
    "Pull-source config for pull streams; null for push streams."
    managed_source: Optional["StreamInNodeDefaultManagedSource"] = Field(
        alias="managedSource",
        description="Safe source summary for managed streams; null for push and pull streams.",
    )
    "Safe source summary for managed streams; null for push and pull streams."
    source_location: "StreamInNodeDefaultSourceLocation" = Field(
        alias="sourceLocation",
        description="Where the stream's source may be ingested, derived from the stream's own ingest placement rules.",
    )
    "Where the stream's source may be ingested, derived from the stream's own ingest placement rules."
    created_at: datetime = Field(
        alias="createdAt", description="When this stream was created."
    )
    "When this stream was created."
    stream_updated_at: datetime = Field(
        alias="streamUpdatedAt", description="When this stream was last modified."
    )
    "When this stream was last modified."
    metrics: Optional["StreamInNodeDefaultMetrics"] = Field(
        description="Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    )
    "Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    push_targets: list["StreamInNodeDefaultPushTargets"] = Field(
        alias="pushTargets",
        description="Configured multistream push targets for this stream.",
    )
    "Configured multistream push targets for this stream."
    playback_policy: Optional["StreamInNodeDefaultPlaybackPolicy"] = Field(
        alias="playbackPolicy",
        description="Playback access policy. null/PUBLIC = anyone with the playbackId can watch.",
    )
    "Playback access policy. null/PUBLIC = anyone with the playbackId can watch."
    dvr_chapter_mode: Optional[DVRChapterMode] = Field(
        alias="dvrChapterMode",
        description="How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast.",
    )
    "How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast."
    dvr_chapter_interval_seconds: Optional[int] = Field(
        alias="dvrChapterIntervalSeconds",
        description="Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour).",
    )
    "Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour)."
    monitoring: MonitoringToggle = Field(
        description="Per-stream Skipper monitoring override (INHERIT follows tier)."
    )
    "Per-stream Skipper monitoring override (INHERIT follows tier)."
    thumbnail_assets: Optional["StreamInNodeDefaultThumbnailAssets"] = Field(
        alias="thumbnailAssets",
        description="Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles.",
    )
    "Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles."
    retention_overrides: Optional["StreamInNodeDefaultRetentionOverrides"] = Field(
        alias="retentionOverrides",
        description="Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here.",
    )
    "Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here."


class StreamInNodeDefaultPullSource(BaseModel):
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


class StreamInNodeDefaultManagedSource(BaseModel):
    """Safe summary of an operator-managed source. Literal paths, commands, and
    credentials are intentionally not exposed through the tenant API."""

    source_kind: str = Field(
        alias="sourceKind", description="Managed source form: file, playlist, or exec."
    )
    "Managed source form: file, playlist, or exec."
    always_on: bool = Field(
        alias="alwaysOn",
        description="Whether the source is kept active without waiting for a viewer.",
    )
    "Whether the source is kept active without waiting for a viewer."
    placement_count: int = Field(
        alias="placementCount",
        description="Number of source placements requested by the operator configuration.",
    )
    "Number of source placements requested by the operator configuration."


class StreamInNodeDefaultSourceLocation(BaseModel):
    mode: SourceLocationMode
    clusters: list["StreamInNodeDefaultSourceLocationClusters"] = Field(
        description="Empty unless mode is RESTRICTED."
    )
    "Empty unless mode is RESTRICTED."
    avoid_node_ids: list[str] = Field(
        alias="avoidNodeIds", description="Empty unless mode is RESTRICTED."
    )
    "Empty unless mode is RESTRICTED."


class StreamInNodeDefaultSourceLocationClusters(BaseModel):
    cluster_id: str = Field(alias="clusterId")
    node_ids: list[str] = Field(
        alias="nodeIds", description="Empty means any node of the cluster."
    )
    "Empty means any node of the cluster."


class StreamInNodeDefaultMetrics(BaseModel):
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
    node_id: Optional[str] = Field(
        alias="nodeId", description="ID of the edge node handling this stream."
    )
    "ID of the edge node handling this stream."
    track_count: Optional[int] = Field(
        alias="trackCount", description="Number of quality tracks available."
    )
    "Number of quality tracks available."
    total_inputs: Optional[int] = Field(
        alias="totalInputs", description="Total ingest connections to this stream."
    )
    "Total ingest connections to this stream."
    uploaded_bytes: float = Field(
        alias="uploadedBytes",
        description="Total bytes uploaded (ingested) for current session.",
    )
    "Total bytes uploaded (ingested) for current session."
    downloaded_bytes: float = Field(
        alias="downloadedBytes",
        description="Total bytes downloaded (egress) for current session.",
    )
    "Total bytes downloaded (egress) for current session."
    viewer_seconds: float = Field(
        alias="viewerSeconds", description="Total viewer-seconds accumulated."
    )
    "Total viewer-seconds accumulated."
    packets_sent: Optional[float] = Field(
        alias="packetsSent", description="Total packets sent to viewers."
    )
    "Total packets sent to viewers."
    packets_lost: Optional[float] = Field(
        alias="packetsLost", description="Total packets lost in transit."
    )
    "Total packets lost in transit."
    packets_retransmitted: Optional[float] = Field(
        alias="packetsRetransmitted", description="Total packets retransmitted."
    )
    "Total packets retransmitted."
    buffer_state: Optional[str] = Field(
        alias="bufferState",
        description="Buffer health state (HEALTHY, WARNING, CRITICAL).",
    )
    "Buffer health state (HEALTHY, WARNING, CRITICAL)."
    quality_tier: Optional[str] = Field(
        alias="qualityTier",
        description="Highest quality tier available (4K, 1080p, 720p, etc.).",
    )
    "Highest quality tier available (4K, 1080p, 720p, etc.)."
    primary_width: Optional[int] = Field(
        alias="primaryWidth", description="Primary video track width in pixels."
    )
    "Primary video track width in pixels."
    primary_height: Optional[int] = Field(
        alias="primaryHeight", description="Primary video track height in pixels."
    )
    "Primary video track height in pixels."
    primary_fps: Optional[float] = Field(
        alias="primaryFps", description="Primary video track framerate."
    )
    "Primary video track framerate."
    primary_codec: Optional[str] = Field(
        alias="primaryCodec",
        description="Primary video codec (H.264, H.265, VP9, AV1).",
    )
    "Primary video codec (H.264, H.265, VP9, AV1)."
    primary_bitrate: Optional[int] = Field(
        alias="primaryBitrate", description="Primary video bitrate in kbps."
    )
    "Primary video bitrate in kbps."
    has_issues: Optional[bool] = Field(
        alias="hasIssues", description="Whether the stream has active quality issues."
    )
    "Whether the stream has active quality issues."
    issues_description: Optional[str] = Field(
        alias="issuesDescription",
        description="Human-readable description of current issues.",
    )
    "Human-readable description of current issues."


class StreamInNodeDefaultPushTargets(BaseModel):
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


class StreamInNodeDefaultPlaybackPolicy(BaseModel):
    """Per-playback-object access policy. Foghorn reads this in the USER_NEW
    trigger handler; the requiresAuth marker (on the playback object itself)
    gates whether the full policy is fetched at all."""

    type_: PlaybackPolicyType = Field(alias="type")
    jwt: Optional["StreamInNodeDefaultPlaybackPolicyJwt"] = Field(
        description="JWT-policy details, populated when type == JWT."
    )
    "JWT-policy details, populated when type == JWT."
    webhook: Optional["StreamInNodeDefaultPlaybackPolicyWebhook"] = Field(
        description="Webhook-policy details, populated when type == WEBHOOK. Secret is masked."
    )
    "Webhook-policy details, populated when type == WEBHOOK. Secret is masked."
    allowed_origins: list[str] = Field(
        alias="allowedOrigins",
        description="Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied.",
    )
    "Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied."


class StreamInNodeDefaultPlaybackPolicyJwt(BaseModel):
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


class StreamInNodeDefaultPlaybackPolicyWebhook(BaseModel):
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
    context: Optional[Any] = Field(
        description="Your JSON object, sent as `context` in every access request to the URL. Null when unset."
    )
    "Your JSON object, sent as `context` in every access request to the URL. Null when unset."


class StreamInNodeDefaultThumbnailAssets(BaseModel):
    """Chandler-served thumbnail asset URLs (poster, sprite, VTT cues). URL shape
    is `{chandlerBase}/assets/{assetKey}/poster.jpg`,
    `/assets/{assetKey}/sprite.jpg`, `/assets/{assetKey}/sprite.vtt` — Chandler
    serves the object key directly, no version resolution. assetKey is stream_id
    for live streams; clip_hash / dvr_hash / vod_hash (= artifact_hash) for artifacts."""

    poster_url: str = Field(alias="posterUrl")
    sprite_vtt_url: str = Field(alias="spriteVttUrl")
    sprite_jpg_url: str = Field(alias="spriteJpgUrl")
    asset_key: str = Field(alias="assetKey")


class StreamInNodeDefaultRetentionOverrides(BaseModel):
    """Per-stream retention overrides for DVR and clips. VOD uploads aren't
    stream-bound, so they have no stream-level knob here."""

    stream_id: str = Field(alias="streamId")
    dvr_retention_days_override: Optional[int] = Field(
        alias="dvrRetentionDaysOverride",
        description="Null = no override (inherit tenant default). 0 = no auto-expire.",
    )
    "Null = no override (inherit tenant default). 0 = no auto-expire."
    clip_retention_days_override: Optional[int] = Field(
        alias="clipRetentionDaysOverride"
    )


class StreamKey(BaseModel):
    typename__: str = Field(alias="__typename")
    id: str
    stream_id: str = Field(alias="streamId")
    key_value: str = Field(
        alias="keyValue",
        description="The publishing secret. Stream keys are read only through operations that need the streams:write scope.",
    )
    "The publishing secret. Stream keys are read only through operations that need the streams:write scope."
    key_name: Optional[str] = Field(alias="keyName")
    is_active: bool = Field(alias="isActive")
    last_used_at: Optional[datetime] = Field(alias="lastUsedAt")
    created_at: datetime = Field(alias="createdAt")


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
    buffer_state: Optional[str] = Field(
        alias="bufferState",
        description="Buffer health state (HEALTHY, WARNING, CRITICAL).",
    )
    "Buffer health state (HEALTHY, WARNING, CRITICAL)."
    quality_tier: Optional[str] = Field(
        alias="qualityTier",
        description="Highest quality tier available (4K, 1080p, 720p, etc.).",
    )
    "Highest quality tier available (4K, 1080p, 720p, etc.)."
    has_issues: Optional[bool] = Field(
        alias="hasIssues", description="Whether the stream has active quality issues."
    )
    "Whether the stream has active quality issues."
    issues_description: Optional[str] = Field(
        alias="issuesDescription",
        description="Human-readable description of current issues.",
    )
    "Human-readable description of current issues."


class StreamRetentionOverridesDefault(BaseModel):
    """Per-stream retention overrides for DVR and clips. VOD uploads aren't
    stream-bound, so they have no stream-level knob here."""

    stream_id: str = Field(alias="streamId")
    dvr_retention_days_override: Optional[int] = Field(
        alias="dvrRetentionDaysOverride",
        description="Null = no override (inherit tenant default). 0 = no auto-expire.",
    )
    "Null = no override (inherit tenant default). 0 = no auto-expire."
    clip_retention_days_override: Optional[int] = Field(
        alias="clipRetentionDaysOverride"
    )


class StreamValidationDefault(BaseModel):
    status: ValidationStatus
    stream_key: str = Field(alias="streamKey")
    error: Optional[str]


class StreamWithKey(Stream):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

    stream_key: Optional[str] = Field(
        alias="streamKey",
        description="Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path.",
    )
    "Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path."


class StreamingConfigDefault(BaseModel):
    preferred_cluster_label: Optional[str] = Field(alias="preferredClusterLabel")
    ingest_domain: Optional[str] = Field(alias="ingestDomain")
    edge_domain: Optional[str] = Field(alias="edgeDomain")
    play_domain: Optional[str] = Field(alias="playDomain")
    official_cluster_label: Optional[str] = Field(alias="officialClusterLabel")
    official_ingest_domain: Optional[str] = Field(alias="officialIngestDomain")
    official_edge_domain: Optional[str] = Field(alias="officialEdgeDomain")
    official_play_domain: Optional[str] = Field(alias="officialPlayDomain")
    global_ingest_domain: Optional[str] = Field(alias="globalIngestDomain")
    global_edge_domain: Optional[str] = Field(alias="globalEdgeDomain")
    global_play_domain: Optional[str] = Field(alias="globalPlayDomain")
    global_livepeer_domain: Optional[str] = Field(alias="globalLivepeerDomain")
    tenant_ingest_domain: Optional[str] = Field(alias="tenantIngestDomain")
    tenant_edge_domain: Optional[str] = Field(alias="tenantEdgeDomain")
    tenant_play_domain: Optional[str] = Field(alias="tenantPlayDomain")
    tenant_livepeer_domain: Optional[str] = Field(alias="tenantLivepeerDomain")
    srt_port: Optional[int] = Field(alias="srtPort")
    rtmp_port: Optional[int] = Field(alias="rtmpPort")


class StripeBillingPortalSessionDefault(BaseModel):
    """Stripe Billing Portal Session - redirect URL for subscription management."""

    portal_url: str = Field(
        alias="portalUrl", description="URL to redirect user to for billing management."
    )
    "URL to redirect user to for billing management."


class StripeCheckoutSessionDefault(BaseModel):
    """Stripe Checkout Session - redirect URL for hosted checkout."""

    session_id: str = Field(
        alias="sessionId", description="Stripe Checkout Session ID (cs_xxx)."
    )
    "Stripe Checkout Session ID (cs_xxx)."
    checkout_url: str = Field(
        alias="checkoutUrl", description="URL to redirect user to for checkout."
    )
    "URL to redirect user to for checkout."


class SystemHealthEventDefault(BaseModel):
    node_id: Optional[str] = Field(alias="nodeId")
    node: str
    location: str
    status: NodeStatus
    cpu_tenths: int = Field(alias="cpuTenths")
    is_healthy: bool = Field(alias="isHealthy")
    ram_max: Optional[float] = Field(alias="ramMax")
    ram_current: Optional[float] = Field(alias="ramCurrent")
    disk_total_bytes: Optional[float] = Field(alias="diskTotalBytes")
    disk_used_bytes: Optional[float] = Field(alias="diskUsedBytes")
    shm_total_bytes: Optional[float] = Field(alias="shmTotalBytes")
    shm_used_bytes: Optional[float] = Field(alias="shmUsedBytes")
    timestamp: datetime


class TenantAnalyticsDailyDefault(BaseModel):
    id: str
    day: datetime
    total_streams: int = Field(alias="totalStreams")
    total_views: int = Field(alias="totalViews")
    unique_viewers: int = Field(alias="uniqueViewers")
    egress_bytes: float = Field(alias="egressBytes")


class TenantDailyStatDefault(BaseModel):
    id: str
    date: datetime
    egress_gb: float = Field(alias="egressGb")
    viewer_hours: float = Field(alias="viewerHours")
    unique_viewers: int = Field(alias="uniqueViewers")
    total_sessions: int = Field(alias="totalSessions")
    total_views: int = Field(alias="totalViews")


class TenantDefault(BaseModel):
    id: str
    name: str
    subdomain: Optional[str] = Field(
        description="Platform-managed tenant DNS label. When set and the tenant alias has issued\nTLS, playback uses foghorn.<subdomain>.cdn.<root> instead of the global\nFoghorn host."
    )
    "Platform-managed tenant DNS label. When set and the tenant alias has issued\nTLS, playback uses foghorn.<subdomain>.cdn.<root> instead of the global\nFoghorn host."
    cluster: Optional[str]
    created_at: datetime = Field(alias="createdAt")
    custom_domain: Optional[str] = Field(
        alias="customDomain",
        description="BYO domain configured for this tenant. Null when the tenant has not opted in.",
    )
    "BYO domain configured for this tenant. Null when the tenant has not opted in."
    custom_domain_status: Optional["TenantDefaultCustomDomainStatus"] = Field(
        alias="customDomainStatus",
        description="Lifecycle state for the BYO domain, surfacing the CNAMEs the operator must\npublish for verification + traffic plus any current error/expiry timestamps.\nNull when no customDomain is configured.",
    )
    "Lifecycle state for the BYO domain, surfacing the CNAMEs the operator must\npublish for verification + traffic plus any current error/expiry timestamps.\nNull when no customDomain is configured."
    monitoring_enabled: bool = Field(
        alias="monitoringEnabled",
        description="Tenant-wide Skipper AI monitoring master switch. Default true.",
    )
    "Tenant-wide Skipper AI monitoring master switch. Default true."


class TenantDefaultCustomDomainStatus(BaseModel):
    """Verification + certificate lifecycle for a tenant's BYO domain. Returned by
    Navigator's GetCustomDomainStatus and surfaced verbatim so the dashboard can
    guide the operator through the manual DNS setup."""

    domain: str = Field(
        description="The domain Navigator is tracking (mirrors Tenant.customDomain)."
    )
    "The domain Navigator is tracking (mirrors Tenant.customDomain)."
    state: str = Field(
        description="pending_verification | verified | pending_alias | cert_issuing | cert_issued |\ncert_failed | verification_failed | tearing_down — verbatim from Navigator.\npending_alias means the CNAMEs are verified and the domain waits for the tenant\nalias certificate. verification_failed means the CNAMEs were not verified\nwithin the fixed verification period."
    )
    "pending_verification | verified | pending_alias | cert_issuing | cert_issued |\ncert_failed | verification_failed | tearing_down — verbatim from Navigator.\npending_alias means the CNAMEs are verified and the domain waits for the tenant\nalias certificate. verification_failed means the CNAMEs were not verified\nwithin the fixed verification period."
    required_traffic_cname: Optional[str] = Field(
        alias="requiredTrafficCname",
        description="CNAME the operator points their public hostname at so the platform's TLS\ningress receives traffic.",
    )
    "CNAME the operator points their public hostname at so the platform's TLS\ningress receives traffic."
    required_acme_challenge_cname: Optional[str] = Field(
        alias="requiredAcmeChallengeCname",
        description="CNAME the operator points `_acme-challenge.<their-domain>` at so Navigator\ncan complete DNS-01 issuance via the `acme-dns.<root>` delegated subzone.",
    )
    "CNAME the operator points `_acme-challenge.<their-domain>` at so Navigator\ncan complete DNS-01 issuance via the `acme-dns.<root>` delegated subzone."
    last_verified_at: Optional[datetime] = Field(
        alias="lastVerifiedAt",
        description="Wall-clock UTC of the last successful verification check (null when never).",
    )
    "Wall-clock UTC of the last successful verification check (null when never)."
    cert_issued_at: Optional[datetime] = Field(
        alias="certIssuedAt",
        description="Wall-clock UTC of the most recent successful cert issuance (null when never).",
    )
    "Wall-clock UTC of the most recent successful cert issuance (null when never)."
    cert_expires_at: Optional[datetime] = Field(
        alias="certExpiresAt",
        description="Wall-clock UTC of the current cert's expiry (null when no cert).",
    )
    "Wall-clock UTC of the current cert's expiry (null when no cert)."
    last_error: Optional[str] = Field(
        alias="lastError",
        description="Most recent error from verification / issuance, null when clean.",
    )
    "Most recent error from verification / issuance, null when clean."


class TenantEventDefault(BaseModel):
    type_: str = Field(alias="type")
    channel: str
    timestamp: datetime
    stream_event: Optional["TenantEventDefaultStreamEvent"] = Field(alias="streamEvent")
    viewer_metrics: Optional["TenantEventDefaultViewerMetrics"] = Field(
        alias="viewerMetrics"
    )
    connection_event: Optional["TenantEventDefaultConnectionEvent"] = Field(
        alias="connectionEvent"
    )
    track_list_update: Optional["TenantEventDefaultTrackListUpdate"] = Field(
        alias="trackListUpdate"
    )
    storage_event: Optional["TenantEventDefaultStorageEvent"] = Field(
        alias="storageEvent"
    )
    storage_snapshot: Optional["TenantEventDefaultStorageSnapshot"] = Field(
        alias="storageSnapshot"
    )
    processing_event: Optional["TenantEventDefaultProcessingEvent"] = Field(
        alias="processingEvent"
    )
    routing_event: Optional["TenantEventDefaultRoutingEvent"] = Field(
        alias="routingEvent"
    )
    system_health_event: Optional["TenantEventDefaultSystemHealthEvent"] = Field(
        alias="systemHealthEvent"
    )
    skipper_investigation: Optional["TenantEventDefaultSkipperInvestigation"] = Field(
        alias="skipperInvestigation"
    )
    incident_updated: Optional["TenantEventDefaultIncidentUpdated"] = Field(
        alias="incidentUpdated"
    )


class TenantEventDefaultStreamEvent(BaseModel):
    id: str
    event_id: str = Field(alias="eventId")
    stream_id: Optional[str] = Field(alias="streamId")
    stream: Optional["TenantEventDefaultStreamEventStream"]
    node_id: Optional[str] = Field(alias="nodeId")
    type_: StreamEventType = Field(alias="type")
    status: Optional[StreamStatus]
    timestamp: datetime
    details: Optional[str]
    payload: Optional[Any]
    source: StreamEventSource
    buffer_state: Optional[str] = Field(alias="bufferState")
    has_issues: Optional[bool] = Field(alias="hasIssues")
    track_count: Optional[int] = Field(alias="trackCount")
    quality_tier: Optional[str] = Field(alias="qualityTier")
    primary_width: Optional[int] = Field(alias="primaryWidth")
    primary_height: Optional[int] = Field(alias="primaryHeight")
    primary_fps: Optional[float] = Field(alias="primaryFps")
    primary_codec: Optional[str] = Field(alias="primaryCodec")
    primary_bitrate: Optional[int] = Field(alias="primaryBitrate")
    downloaded_bytes: Optional[float] = Field(alias="downloadedBytes")
    uploaded_bytes: Optional[float] = Field(alias="uploadedBytes")
    total_viewers: Optional[int] = Field(alias="totalViewers")
    total_inputs: Optional[int] = Field(alias="totalInputs")
    total_outputs: Optional[int] = Field(alias="totalOutputs")
    viewer_seconds: Optional[float] = Field(alias="viewerSeconds")
    request_url: Optional[str] = Field(alias="requestUrl")
    protocol: Optional[str]
    latitude: Optional[float]
    longitude: Optional[float]
    location: Optional[str]
    country_code: Optional[str] = Field(alias="countryCode")
    city: Optional[str]
    source_region: str = Field(alias="sourceRegion")
    source_cluster_id: str = Field(alias="sourceClusterId")
    stream_origin_region: str = Field(alias="streamOriginRegion")
    stream_origin_cluster_id: str = Field(alias="streamOriginClusterId")
    schema_version: int = Field(alias="schemaVersion")


class TenantEventDefaultStreamEventStream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

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
        description="Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path.",
    )
    "Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path."
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
        description="How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast.",
    )
    "How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast."
    dvr_chapter_interval_seconds: Optional[int] = Field(
        alias="dvrChapterIntervalSeconds",
        description="Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour).",
    )
    "Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour)."
    monitoring: MonitoringToggle = Field(
        description="Per-stream Skipper monitoring override (INHERIT follows tier)."
    )
    "Per-stream Skipper monitoring override (INHERIT follows tier)."


class TenantEventDefaultViewerMetrics(BaseModel):
    node_id: str = Field(alias="nodeId")
    stream_id: str = Field(alias="streamId")
    stream: Optional["TenantEventDefaultViewerMetricsStream"]
    action: str
    protocol: str
    host: Optional[str]
    session_id: Optional[str] = Field(alias="sessionId")
    connection_time: Optional[float] = Field(alias="connectionTime")
    position: Optional[float]
    bandwidth_in_bps: Optional[int] = Field(alias="bandwidthInBps")
    bandwidth_out_bps: Optional[int] = Field(alias="bandwidthOutBps")
    bytes_downloaded: Optional[float] = Field(alias="bytesDownloaded")
    bytes_uploaded: Optional[float] = Field(alias="bytesUploaded")
    packets_sent: Optional[int] = Field(alias="packetsSent")
    packets_lost: Optional[int] = Field(alias="packetsLost")
    packets_retransmitted: Optional[int] = Field(alias="packetsRetransmitted")
    timestamp: int
    client_country: Optional[str] = Field(alias="clientCountry")
    client_city: Optional[str] = Field(alias="clientCity")
    client_latitude: Optional[float] = Field(alias="clientLatitude")
    client_longitude: Optional[float] = Field(alias="clientLongitude")


class TenantEventDefaultViewerMetricsStream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

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
        description="Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path.",
    )
    "Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path."
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
        description="How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast.",
    )
    "How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast."
    dvr_chapter_interval_seconds: Optional[int] = Field(
        alias="dvrChapterIntervalSeconds",
        description="Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour).",
    )
    "Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour)."
    monitoring: MonitoringToggle = Field(
        description="Per-stream Skipper monitoring override (INHERIT follows tier)."
    )
    "Per-stream Skipper monitoring override (INHERIT follows tier)."


class TenantEventDefaultConnectionEvent(BaseModel):
    id: str
    event_id: str = Field(alias="eventId")
    timestamp: datetime
    stream_id: str = Field(alias="streamId")
    stream: Optional["TenantEventDefaultConnectionEventStream"]
    session_id: str = Field(alias="sessionId")
    connection_addr: Optional[str] = Field(alias="connectionAddr")
    connector: str
    node_id: str = Field(alias="nodeId")
    country_code: Optional[str] = Field(alias="countryCode")
    city: Optional[str]
    latitude: Optional[float]
    longitude: Optional[float]
    client_bucket: Optional["TenantEventDefaultConnectionEventClientBucket"] = Field(
        alias="clientBucket"
    )
    node_bucket: Optional["TenantEventDefaultConnectionEventNodeBucket"] = Field(
        alias="nodeBucket"
    )
    event_type: str = Field(alias="eventType")
    request_url: Optional[str] = Field(alias="requestUrl")
    cluster_id: str = Field(alias="clusterId")
    origin_cluster_id: Optional[str] = Field(alias="originClusterId")
    control_cell_id: Optional[str] = Field(alias="controlCellId")
    session_duration_seconds: Optional[int] = Field(alias="sessionDurationSeconds")
    bytes_transferred: Optional[float] = Field(alias="bytesTransferred")


class TenantEventDefaultConnectionEventStream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

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
        description="Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path.",
    )
    "Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path."
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
        description="How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast.",
    )
    "How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast."
    dvr_chapter_interval_seconds: Optional[int] = Field(
        alias="dvrChapterIntervalSeconds",
        description="Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour).",
    )
    "Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour)."
    monitoring: MonitoringToggle = Field(
        description="Per-stream Skipper monitoring override (INHERIT follows tier)."
    )
    "Per-stream Skipper monitoring override (INHERIT follows tier)."


class TenantEventDefaultConnectionEventClientBucket(BaseModel):
    h_3_index: str = Field(alias="h3Index")
    resolution: int


class TenantEventDefaultConnectionEventNodeBucket(BaseModel):
    h_3_index: str = Field(alias="h3Index")
    resolution: int


class TenantEventDefaultTrackListUpdate(BaseModel):
    stream_id: str = Field(alias="streamId")
    stream: Optional["TenantEventDefaultTrackListUpdateStream"]
    tracks: Optional[list["TenantEventDefaultTrackListUpdateTracks"]]
    total_tracks: Optional[int] = Field(alias="totalTracks")
    video_track_count: Optional[int] = Field(alias="videoTrackCount")
    audio_track_count: Optional[int] = Field(alias="audioTrackCount")
    quality_tier: Optional[str] = Field(alias="qualityTier")
    primary_width: Optional[int] = Field(alias="primaryWidth")
    primary_height: Optional[int] = Field(alias="primaryHeight")
    primary_fps: Optional[float] = Field(alias="primaryFps")
    primary_video_bitrate: Optional[int] = Field(alias="primaryVideoBitrate")
    primary_video_codec: Optional[str] = Field(alias="primaryVideoCodec")
    primary_audio_bitrate: Optional[int] = Field(alias="primaryAudioBitrate")
    primary_audio_codec: Optional[str] = Field(alias="primaryAudioCodec")
    primary_audio_channels: Optional[int] = Field(alias="primaryAudioChannels")
    primary_audio_sample_rate: Optional[int] = Field(alias="primaryAudioSampleRate")


class TenantEventDefaultTrackListUpdateStream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

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
        description="Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path.",
    )
    "Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path."
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
        description="How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast.",
    )
    "How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast."
    dvr_chapter_interval_seconds: Optional[int] = Field(
        alias="dvrChapterIntervalSeconds",
        description="Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour).",
    )
    "Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour)."
    monitoring: MonitoringToggle = Field(
        description="Per-stream Skipper monitoring override (INHERIT follows tier)."
    )
    "Per-stream Skipper monitoring override (INHERIT follows tier)."


class TenantEventDefaultTrackListUpdateTracks(BaseModel):
    track_name: str = Field(alias="trackName")
    track_type: str = Field(alias="trackType")
    codec: Optional[str]
    bitrate_kbps: Optional[int] = Field(alias="bitrateKbps")
    bitrate_bps: Optional[int] = Field(alias="bitrateBps")
    buffer: Optional[int]
    jitter: Optional[int]
    width: Optional[int]
    height: Optional[int]
    fps: Optional[float]
    resolution: Optional[str]
    has_b_frames: Optional[bool] = Field(alias="hasBFrames")
    channels: Optional[int]
    sample_rate: Optional[int] = Field(alias="sampleRate")


class TenantEventDefaultStorageEvent(BaseModel):
    id: str
    timestamp: datetime
    stream_id: str = Field(alias="streamId")
    stream: Optional["TenantEventDefaultStorageEventStream"]
    asset_hash: str = Field(alias="assetHash")
    action: str
    asset_type: str = Field(alias="assetType")
    size_bytes: float = Field(alias="sizeBytes")
    s_3_url: Optional[str] = Field(alias="s3Url")
    local_path: Optional[str] = Field(alias="localPath")
    node_id: str = Field(alias="nodeId")
    cluster_id: Optional[str] = Field(alias="clusterId")
    origin_cluster_id: Optional[str] = Field(alias="originClusterId")
    control_cell_id: Optional[str] = Field(alias="controlCellId")
    duration_ms: Optional[int] = Field(alias="durationMs")
    warm_duration_ms: Optional[int] = Field(alias="warmDurationMs")
    error: Optional[str]


class TenantEventDefaultStorageEventStream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

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
        description="Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path.",
    )
    "Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path."
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
        description="How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast.",
    )
    "How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast."
    dvr_chapter_interval_seconds: Optional[int] = Field(
        alias="dvrChapterIntervalSeconds",
        description="Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour).",
    )
    "Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour)."
    monitoring: MonitoringToggle = Field(
        description="Per-stream Skipper monitoring override (INHERIT follows tier)."
    )
    "Per-stream Skipper monitoring override (INHERIT follows tier)."


class TenantEventDefaultStorageSnapshot(BaseModel):
    node_id: str = Field(alias="nodeId")
    timestamp: int
    tenant_id: Optional[str] = Field(alias="tenantId")
    location: Optional[str]
    storage_scope: Optional[str] = Field(alias="storageScope")
    usage: list["TenantEventDefaultStorageSnapshotUsage"]


class TenantEventDefaultStorageSnapshotUsage(BaseModel):
    tenant_id: str = Field(alias="tenantId")
    total_bytes: float = Field(alias="totalBytes")
    file_count: int = Field(alias="fileCount")
    dvr_bytes: float = Field(alias="dvrBytes")
    clip_bytes: float = Field(alias="clipBytes")
    vod_bytes: float = Field(alias="vodBytes")
    frozen_dvr_bytes: float = Field(alias="frozenDvrBytes")
    frozen_clip_bytes: float = Field(alias="frozenClipBytes")
    frozen_vod_bytes: float = Field(alias="frozenVodBytes")


class TenantEventDefaultProcessingEvent(BaseModel):
    id: str
    timestamp: datetime
    node_id: str = Field(alias="nodeId")
    stream_id: str = Field(alias="streamId")
    stream: Optional["TenantEventDefaultProcessingEventStream"]
    process_type: str = Field(alias="processType")
    cluster_id: Optional[str] = Field(alias="clusterId")
    origin_cluster_id: Optional[str] = Field(alias="originClusterId")
    control_cell_id: Optional[str] = Field(alias="controlCellId")
    track_type: Optional[str] = Field(alias="trackType")
    duration_ms: int = Field(alias="durationMs")
    input_codec: Optional[str] = Field(alias="inputCodec")
    output_codec: Optional[str] = Field(alias="outputCodec")
    segment_number: Optional[int] = Field(alias="segmentNumber")
    width: Optional[int]
    height: Optional[int]
    rendition_count: Optional[int] = Field(alias="renditionCount")
    broadcaster_url: Optional[str] = Field(alias="broadcasterUrl")
    upload_time_us: Optional[int] = Field(alias="uploadTimeUs")
    livepeer_session_id: Optional[str] = Field(alias="livepeerSessionId")
    segment_start_ms: Optional[int] = Field(alias="segmentStartMs")
    input_bytes: Optional[float] = Field(alias="inputBytes")
    output_bytes_total: Optional[float] = Field(alias="outputBytesTotal")
    attempt_count: Optional[int] = Field(alias="attemptCount")
    turnaround_ms: Optional[int] = Field(alias="turnaroundMs")
    speed_factor: Optional[float] = Field(alias="speedFactor")
    renditions_json: Optional[str] = Field(alias="renditionsJson")
    input_frames: Optional[int] = Field(alias="inputFrames")
    output_frames: Optional[int] = Field(alias="outputFrames")
    decode_us_per_frame: Optional[int] = Field(alias="decodeUsPerFrame")
    transform_us_per_frame: Optional[int] = Field(alias="transformUsPerFrame")
    encode_us_per_frame: Optional[int] = Field(alias="encodeUsPerFrame")
    is_final: Optional[bool] = Field(alias="isFinal")
    input_frames_delta: Optional[int] = Field(alias="inputFramesDelta")
    output_frames_delta: Optional[int] = Field(alias="outputFramesDelta")
    input_bytes_delta: Optional[float] = Field(alias="inputBytesDelta")
    output_bytes_delta: Optional[float] = Field(alias="outputBytesDelta")
    input_width: Optional[int] = Field(alias="inputWidth")
    input_height: Optional[int] = Field(alias="inputHeight")
    output_width: Optional[int] = Field(alias="outputWidth")
    output_height: Optional[int] = Field(alias="outputHeight")
    input_fpks: Optional[int] = Field(alias="inputFpks")
    output_fps_measured: Optional[float] = Field(alias="outputFpsMeasured")
    sample_rate: Optional[int] = Field(alias="sampleRate")
    channels: Optional[int]
    source_timestamp_ms: Optional[int] = Field(alias="sourceTimestampMs")
    sink_timestamp_ms: Optional[int] = Field(alias="sinkTimestampMs")
    source_advanced_ms: Optional[int] = Field(alias="sourceAdvancedMs")
    sink_advanced_ms: Optional[int] = Field(alias="sinkAdvancedMs")
    rtf_in: Optional[float] = Field(alias="rtfIn")
    rtf_out: Optional[float] = Field(alias="rtfOut")
    pipeline_lag_ms: Optional[int] = Field(alias="pipelineLagMs")
    output_bitrate_bps: Optional[int] = Field(alias="outputBitrateBps")


class TenantEventDefaultProcessingEventStream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

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
        description="Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path.",
    )
    "Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path."
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
        description="How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast.",
    )
    "How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast."
    dvr_chapter_interval_seconds: Optional[int] = Field(
        alias="dvrChapterIntervalSeconds",
        description="Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour).",
    )
    "Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour)."
    monitoring: MonitoringToggle = Field(
        description="Per-stream Skipper monitoring override (INHERIT follows tier)."
    )
    "Per-stream Skipper monitoring override (INHERIT follows tier)."


class TenantEventDefaultRoutingEvent(BaseModel):
    timestamp: datetime
    stream_id: str = Field(alias="streamId")
    stream: Optional["TenantEventDefaultRoutingEventStream"]
    selected_node: str = Field(alias="selectedNode")
    node_id: Optional[str] = Field(alias="nodeId")
    status: str
    details: Optional[str]
    score: Optional[int]
    client_country: Optional[str] = Field(alias="clientCountry")
    client_latitude: Optional[float] = Field(alias="clientLatitude")
    client_longitude: Optional[float] = Field(alias="clientLongitude")
    client_bucket: Optional["TenantEventDefaultRoutingEventClientBucket"] = Field(
        alias="clientBucket"
    )
    node_latitude: Optional[float] = Field(alias="nodeLatitude")
    node_longitude: Optional[float] = Field(alias="nodeLongitude")
    node_name: Optional[str] = Field(alias="nodeName")
    node_bucket: Optional["TenantEventDefaultRoutingEventNodeBucket"] = Field(
        alias="nodeBucket"
    )
    routing_distance: Optional[float] = Field(alias="routingDistance")
    candidates_count: Optional[int] = Field(alias="candidatesCount")
    latency_ms: Optional[float] = Field(alias="latencyMs")
    event_type: Optional[str] = Field(alias="eventType")
    source: Optional[str]
    stream_tenant_id: Optional[str] = Field(alias="streamTenantId")
    cluster_id: Optional[str] = Field(alias="clusterId")
    remote_cluster_id: Optional[str] = Field(alias="remoteClusterId")
    selected_cluster_id: Optional[str] = Field(alias="selectedClusterId")
    control_cell_id: Optional[str] = Field(alias="controlCellId")
    origin_cluster_id: Optional[str] = Field(alias="originClusterId")


class TenantEventDefaultRoutingEventStream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

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
        description="Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path.",
    )
    "Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path."
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
        description="How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast.",
    )
    "How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast."
    dvr_chapter_interval_seconds: Optional[int] = Field(
        alias="dvrChapterIntervalSeconds",
        description="Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour).",
    )
    "Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour)."
    monitoring: MonitoringToggle = Field(
        description="Per-stream Skipper monitoring override (INHERIT follows tier)."
    )
    "Per-stream Skipper monitoring override (INHERIT follows tier)."


class TenantEventDefaultRoutingEventClientBucket(BaseModel):
    h_3_index: str = Field(alias="h3Index")
    resolution: int


class TenantEventDefaultRoutingEventNodeBucket(BaseModel):
    h_3_index: str = Field(alias="h3Index")
    resolution: int


class TenantEventDefaultSystemHealthEvent(BaseModel):
    node_id: Optional[str] = Field(alias="nodeId")
    node: str
    location: str
    status: NodeStatus
    cpu_tenths: int = Field(alias="cpuTenths")
    is_healthy: bool = Field(alias="isHealthy")
    ram_max: Optional[float] = Field(alias="ramMax")
    ram_current: Optional[float] = Field(alias="ramCurrent")
    disk_total_bytes: Optional[float] = Field(alias="diskTotalBytes")
    disk_used_bytes: Optional[float] = Field(alias="diskUsedBytes")
    shm_total_bytes: Optional[float] = Field(alias="shmTotalBytes")
    shm_used_bytes: Optional[float] = Field(alias="shmUsedBytes")
    timestamp: datetime


class TenantEventDefaultSkipperInvestigation(BaseModel):
    report_id: str = Field(alias="reportId")
    resource_type: str = Field(alias="resourceType")


class TenantEventDefaultIncidentUpdated(BaseModel):
    """Change to an incident the subscriber can see."""

    incident_id: str = Field(alias="incidentId")
    cluster_id: Optional[str] = Field(alias="clusterId")
    status: IncidentStatus
    severity: str
    title: str
    change: str = Field(
        description="Timeline change that produced the update, e.g. opened, alert_firing, acknowledged, resolved."
    )
    "Timeline change that produced the update, e.g. opened, alert_firing, acknowledged, resolved."
    updated_at: datetime = Field(alias="updatedAt")


class TopAssetEntryDefault(BaseModel):
    """One ranked asset for the Top Assets surface — cross-kind, ranked server-side by
    audience sessions in the window. `kind` badges the asset type; title/playbackId are
    composed from the catalog. (Named distinctly from the periscope proto TopAsset to
    avoid gqlgen autobinding to that message.)"""

    artifact_hash: str = Field(alias="artifactHash")
    kind: StorageArtifactKind
    total_sessions: int = Field(alias="totalSessions")
    watch_hours: float = Field(alias="watchHours")
    duration_s: int = Field(alias="durationS")
    title: Optional[str]
    playback_id: Optional[str] = Field(alias="playbackId")


class TrackListEventDefault(BaseModel):
    id: str
    stream_id: str = Field(alias="streamId")
    stream: Optional["TrackListEventDefaultStream"]
    node_id: Optional[str] = Field(alias="nodeId")
    track_list: str = Field(alias="trackList")
    track_count: int = Field(alias="trackCount")
    timestamp: datetime
    tracks: Optional[list["TrackListEventDefaultTracks"]]


class TrackListEventDefaultStream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

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
        description="Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path.",
    )
    "Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path."
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
    pull_source: Optional["TrackListEventDefaultStreamPullSource"] = Field(
        alias="pullSource",
        description="Pull-source config for pull streams; null for push streams.",
    )
    "Pull-source config for pull streams; null for push streams."
    managed_source: Optional["TrackListEventDefaultStreamManagedSource"] = Field(
        alias="managedSource",
        description="Safe source summary for managed streams; null for push and pull streams.",
    )
    "Safe source summary for managed streams; null for push and pull streams."
    source_location: "TrackListEventDefaultStreamSourceLocation" = Field(
        alias="sourceLocation",
        description="Where the stream's source may be ingested, derived from the stream's own ingest placement rules.",
    )
    "Where the stream's source may be ingested, derived from the stream's own ingest placement rules."
    created_at: datetime = Field(
        alias="createdAt", description="When this stream was created."
    )
    "When this stream was created."
    updated_at: datetime = Field(
        alias="updatedAt", description="When this stream was last modified."
    )
    "When this stream was last modified."
    metrics: Optional["TrackListEventDefaultStreamMetrics"] = Field(
        description="Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    )
    "Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    push_targets: list["TrackListEventDefaultStreamPushTargets"] = Field(
        alias="pushTargets",
        description="Configured multistream push targets for this stream.",
    )
    "Configured multistream push targets for this stream."
    playback_policy: Optional["TrackListEventDefaultStreamPlaybackPolicy"] = Field(
        alias="playbackPolicy",
        description="Playback access policy. null/PUBLIC = anyone with the playbackId can watch.",
    )
    "Playback access policy. null/PUBLIC = anyone with the playbackId can watch."
    dvr_chapter_mode: Optional[DVRChapterMode] = Field(
        alias="dvrChapterMode",
        description="How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast.",
    )
    "How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast."
    dvr_chapter_interval_seconds: Optional[int] = Field(
        alias="dvrChapterIntervalSeconds",
        description="Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour).",
    )
    "Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour)."
    monitoring: MonitoringToggle = Field(
        description="Per-stream Skipper monitoring override (INHERIT follows tier)."
    )
    "Per-stream Skipper monitoring override (INHERIT follows tier)."
    thumbnail_assets: Optional["TrackListEventDefaultStreamThumbnailAssets"] = Field(
        alias="thumbnailAssets",
        description="Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles.",
    )
    "Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles."
    retention_overrides: Optional["TrackListEventDefaultStreamRetentionOverrides"] = (
        Field(
            alias="retentionOverrides",
            description="Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here.",
        )
    )
    "Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here."


class TrackListEventDefaultStreamPullSource(BaseModel):
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


class TrackListEventDefaultStreamManagedSource(BaseModel):
    """Safe summary of an operator-managed source. Literal paths, commands, and
    credentials are intentionally not exposed through the tenant API."""

    source_kind: str = Field(
        alias="sourceKind", description="Managed source form: file, playlist, or exec."
    )
    "Managed source form: file, playlist, or exec."
    always_on: bool = Field(
        alias="alwaysOn",
        description="Whether the source is kept active without waiting for a viewer.",
    )
    "Whether the source is kept active without waiting for a viewer."
    placement_count: int = Field(
        alias="placementCount",
        description="Number of source placements requested by the operator configuration.",
    )
    "Number of source placements requested by the operator configuration."


class TrackListEventDefaultStreamSourceLocation(BaseModel):
    mode: SourceLocationMode
    avoid_node_ids: list[str] = Field(
        alias="avoidNodeIds", description="Empty unless mode is RESTRICTED."
    )
    "Empty unless mode is RESTRICTED."


class TrackListEventDefaultStreamMetrics(BaseModel):
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
    node_id: Optional[str] = Field(
        alias="nodeId", description="ID of the edge node handling this stream."
    )
    "ID of the edge node handling this stream."
    track_count: Optional[int] = Field(
        alias="trackCount", description="Number of quality tracks available."
    )
    "Number of quality tracks available."
    total_inputs: Optional[int] = Field(
        alias="totalInputs", description="Total ingest connections to this stream."
    )
    "Total ingest connections to this stream."
    uploaded_bytes: float = Field(
        alias="uploadedBytes",
        description="Total bytes uploaded (ingested) for current session.",
    )
    "Total bytes uploaded (ingested) for current session."
    downloaded_bytes: float = Field(
        alias="downloadedBytes",
        description="Total bytes downloaded (egress) for current session.",
    )
    "Total bytes downloaded (egress) for current session."
    viewer_seconds: float = Field(
        alias="viewerSeconds", description="Total viewer-seconds accumulated."
    )
    "Total viewer-seconds accumulated."
    packets_sent: Optional[float] = Field(
        alias="packetsSent", description="Total packets sent to viewers."
    )
    "Total packets sent to viewers."
    packets_lost: Optional[float] = Field(
        alias="packetsLost", description="Total packets lost in transit."
    )
    "Total packets lost in transit."
    packets_retransmitted: Optional[float] = Field(
        alias="packetsRetransmitted", description="Total packets retransmitted."
    )
    "Total packets retransmitted."
    buffer_state: Optional[str] = Field(
        alias="bufferState",
        description="Buffer health state (HEALTHY, WARNING, CRITICAL).",
    )
    "Buffer health state (HEALTHY, WARNING, CRITICAL)."
    quality_tier: Optional[str] = Field(
        alias="qualityTier",
        description="Highest quality tier available (4K, 1080p, 720p, etc.).",
    )
    "Highest quality tier available (4K, 1080p, 720p, etc.)."
    primary_width: Optional[int] = Field(
        alias="primaryWidth", description="Primary video track width in pixels."
    )
    "Primary video track width in pixels."
    primary_height: Optional[int] = Field(
        alias="primaryHeight", description="Primary video track height in pixels."
    )
    "Primary video track height in pixels."
    primary_fps: Optional[float] = Field(
        alias="primaryFps", description="Primary video track framerate."
    )
    "Primary video track framerate."
    primary_codec: Optional[str] = Field(
        alias="primaryCodec",
        description="Primary video codec (H.264, H.265, VP9, AV1).",
    )
    "Primary video codec (H.264, H.265, VP9, AV1)."
    primary_bitrate: Optional[int] = Field(
        alias="primaryBitrate", description="Primary video bitrate in kbps."
    )
    "Primary video bitrate in kbps."
    has_issues: Optional[bool] = Field(
        alias="hasIssues", description="Whether the stream has active quality issues."
    )
    "Whether the stream has active quality issues."
    issues_description: Optional[str] = Field(
        alias="issuesDescription",
        description="Human-readable description of current issues.",
    )
    "Human-readable description of current issues."


class TrackListEventDefaultStreamPushTargets(BaseModel):
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


class TrackListEventDefaultStreamPlaybackPolicy(BaseModel):
    """Per-playback-object access policy. Foghorn reads this in the USER_NEW
    trigger handler; the requiresAuth marker (on the playback object itself)
    gates whether the full policy is fetched at all."""

    type_: PlaybackPolicyType = Field(alias="type")
    allowed_origins: list[str] = Field(
        alias="allowedOrigins",
        description="Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied.",
    )
    "Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied."


class TrackListEventDefaultStreamThumbnailAssets(BaseModel):
    """Chandler-served thumbnail asset URLs (poster, sprite, VTT cues). URL shape
    is `{chandlerBase}/assets/{assetKey}/poster.jpg`,
    `/assets/{assetKey}/sprite.jpg`, `/assets/{assetKey}/sprite.vtt` — Chandler
    serves the object key directly, no version resolution. assetKey is stream_id
    for live streams; clip_hash / dvr_hash / vod_hash (= artifact_hash) for artifacts."""

    poster_url: str = Field(alias="posterUrl")
    sprite_vtt_url: str = Field(alias="spriteVttUrl")
    sprite_jpg_url: str = Field(alias="spriteJpgUrl")
    asset_key: str = Field(alias="assetKey")


class TrackListEventDefaultStreamRetentionOverrides(BaseModel):
    """Per-stream retention overrides for DVR and clips. VOD uploads aren't
    stream-bound, so they have no stream-level knob here."""

    stream_id: str = Field(alias="streamId")
    dvr_retention_days_override: Optional[int] = Field(
        alias="dvrRetentionDaysOverride",
        description="Null = no override (inherit tenant default). 0 = no auto-expire.",
    )
    "Null = no override (inherit tenant default). 0 = no auto-expire."
    clip_retention_days_override: Optional[int] = Field(
        alias="clipRetentionDaysOverride"
    )


class TrackListEventDefaultTracks(BaseModel):
    track_name: str = Field(alias="trackName")
    track_type: str = Field(alias="trackType")
    codec: Optional[str]
    bitrate_kbps: Optional[int] = Field(alias="bitrateKbps")
    bitrate_bps: Optional[int] = Field(alias="bitrateBps")
    buffer: Optional[int]
    jitter: Optional[int]
    width: Optional[int]
    height: Optional[int]
    fps: Optional[float]
    resolution: Optional[str]
    has_b_frames: Optional[bool] = Field(alias="hasBFrames")
    channels: Optional[int]
    sample_rate: Optional[int] = Field(alias="sampleRate")


class TrackListEventInNodeDefault(BaseModel):
    id: str
    stream_id: str = Field(alias="streamId")
    stream: Optional["TrackListEventInNodeDefaultStream"]
    track_list_event_node_id: Optional[str] = Field(alias="trackListEventNodeId")
    track_list: str = Field(alias="trackList")
    track_list_event_track_count: int = Field(alias="trackListEventTrackCount")
    timestamp: datetime
    tracks: Optional[list["TrackListEventInNodeDefaultTracks"]]


class TrackListEventInNodeDefaultStream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

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
        description="Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path.",
    )
    "Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path."
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
    pull_source: Optional["TrackListEventInNodeDefaultStreamPullSource"] = Field(
        alias="pullSource",
        description="Pull-source config for pull streams; null for push streams.",
    )
    "Pull-source config for pull streams; null for push streams."
    managed_source: Optional["TrackListEventInNodeDefaultStreamManagedSource"] = Field(
        alias="managedSource",
        description="Safe source summary for managed streams; null for push and pull streams.",
    )
    "Safe source summary for managed streams; null for push and pull streams."
    source_location: "TrackListEventInNodeDefaultStreamSourceLocation" = Field(
        alias="sourceLocation",
        description="Where the stream's source may be ingested, derived from the stream's own ingest placement rules.",
    )
    "Where the stream's source may be ingested, derived from the stream's own ingest placement rules."
    created_at: datetime = Field(
        alias="createdAt", description="When this stream was created."
    )
    "When this stream was created."
    updated_at: datetime = Field(
        alias="updatedAt", description="When this stream was last modified."
    )
    "When this stream was last modified."
    metrics: Optional["TrackListEventInNodeDefaultStreamMetrics"] = Field(
        description="Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    )
    "Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    push_targets: list["TrackListEventInNodeDefaultStreamPushTargets"] = Field(
        alias="pushTargets",
        description="Configured multistream push targets for this stream.",
    )
    "Configured multistream push targets for this stream."
    playback_policy: Optional["TrackListEventInNodeDefaultStreamPlaybackPolicy"] = (
        Field(
            alias="playbackPolicy",
            description="Playback access policy. null/PUBLIC = anyone with the playbackId can watch.",
        )
    )
    "Playback access policy. null/PUBLIC = anyone with the playbackId can watch."
    dvr_chapter_mode: Optional[DVRChapterMode] = Field(
        alias="dvrChapterMode",
        description="How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast.",
    )
    "How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast."
    dvr_chapter_interval_seconds: Optional[int] = Field(
        alias="dvrChapterIntervalSeconds",
        description="Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour).",
    )
    "Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour)."
    monitoring: MonitoringToggle = Field(
        description="Per-stream Skipper monitoring override (INHERIT follows tier)."
    )
    "Per-stream Skipper monitoring override (INHERIT follows tier)."
    thumbnail_assets: Optional["TrackListEventInNodeDefaultStreamThumbnailAssets"] = (
        Field(
            alias="thumbnailAssets",
            description="Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles.",
        )
    )
    "Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles."
    retention_overrides: Optional[
        "TrackListEventInNodeDefaultStreamRetentionOverrides"
    ] = Field(
        alias="retentionOverrides",
        description="Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here.",
    )
    "Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here."


class TrackListEventInNodeDefaultStreamPullSource(BaseModel):
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


class TrackListEventInNodeDefaultStreamManagedSource(BaseModel):
    """Safe summary of an operator-managed source. Literal paths, commands, and
    credentials are intentionally not exposed through the tenant API."""

    source_kind: str = Field(
        alias="sourceKind", description="Managed source form: file, playlist, or exec."
    )
    "Managed source form: file, playlist, or exec."
    always_on: bool = Field(
        alias="alwaysOn",
        description="Whether the source is kept active without waiting for a viewer.",
    )
    "Whether the source is kept active without waiting for a viewer."
    placement_count: int = Field(
        alias="placementCount",
        description="Number of source placements requested by the operator configuration.",
    )
    "Number of source placements requested by the operator configuration."


class TrackListEventInNodeDefaultStreamSourceLocation(BaseModel):
    mode: SourceLocationMode
    avoid_node_ids: list[str] = Field(
        alias="avoidNodeIds", description="Empty unless mode is RESTRICTED."
    )
    "Empty unless mode is RESTRICTED."


class TrackListEventInNodeDefaultStreamMetrics(BaseModel):
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
    node_id: Optional[str] = Field(
        alias="nodeId", description="ID of the edge node handling this stream."
    )
    "ID of the edge node handling this stream."
    track_count: Optional[int] = Field(
        alias="trackCount", description="Number of quality tracks available."
    )
    "Number of quality tracks available."
    total_inputs: Optional[int] = Field(
        alias="totalInputs", description="Total ingest connections to this stream."
    )
    "Total ingest connections to this stream."
    uploaded_bytes: float = Field(
        alias="uploadedBytes",
        description="Total bytes uploaded (ingested) for current session.",
    )
    "Total bytes uploaded (ingested) for current session."
    downloaded_bytes: float = Field(
        alias="downloadedBytes",
        description="Total bytes downloaded (egress) for current session.",
    )
    "Total bytes downloaded (egress) for current session."
    viewer_seconds: float = Field(
        alias="viewerSeconds", description="Total viewer-seconds accumulated."
    )
    "Total viewer-seconds accumulated."
    packets_sent: Optional[float] = Field(
        alias="packetsSent", description="Total packets sent to viewers."
    )
    "Total packets sent to viewers."
    packets_lost: Optional[float] = Field(
        alias="packetsLost", description="Total packets lost in transit."
    )
    "Total packets lost in transit."
    packets_retransmitted: Optional[float] = Field(
        alias="packetsRetransmitted", description="Total packets retransmitted."
    )
    "Total packets retransmitted."
    buffer_state: Optional[str] = Field(
        alias="bufferState",
        description="Buffer health state (HEALTHY, WARNING, CRITICAL).",
    )
    "Buffer health state (HEALTHY, WARNING, CRITICAL)."
    quality_tier: Optional[str] = Field(
        alias="qualityTier",
        description="Highest quality tier available (4K, 1080p, 720p, etc.).",
    )
    "Highest quality tier available (4K, 1080p, 720p, etc.)."
    primary_width: Optional[int] = Field(
        alias="primaryWidth", description="Primary video track width in pixels."
    )
    "Primary video track width in pixels."
    primary_height: Optional[int] = Field(
        alias="primaryHeight", description="Primary video track height in pixels."
    )
    "Primary video track height in pixels."
    primary_fps: Optional[float] = Field(
        alias="primaryFps", description="Primary video track framerate."
    )
    "Primary video track framerate."
    primary_codec: Optional[str] = Field(
        alias="primaryCodec",
        description="Primary video codec (H.264, H.265, VP9, AV1).",
    )
    "Primary video codec (H.264, H.265, VP9, AV1)."
    primary_bitrate: Optional[int] = Field(
        alias="primaryBitrate", description="Primary video bitrate in kbps."
    )
    "Primary video bitrate in kbps."
    has_issues: Optional[bool] = Field(
        alias="hasIssues", description="Whether the stream has active quality issues."
    )
    "Whether the stream has active quality issues."
    issues_description: Optional[str] = Field(
        alias="issuesDescription",
        description="Human-readable description of current issues.",
    )
    "Human-readable description of current issues."


class TrackListEventInNodeDefaultStreamPushTargets(BaseModel):
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


class TrackListEventInNodeDefaultStreamPlaybackPolicy(BaseModel):
    """Per-playback-object access policy. Foghorn reads this in the USER_NEW
    trigger handler; the requiresAuth marker (on the playback object itself)
    gates whether the full policy is fetched at all."""

    type_: PlaybackPolicyType = Field(alias="type")
    allowed_origins: list[str] = Field(
        alias="allowedOrigins",
        description="Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied.",
    )
    "Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied."


class TrackListEventInNodeDefaultStreamThumbnailAssets(BaseModel):
    """Chandler-served thumbnail asset URLs (poster, sprite, VTT cues). URL shape
    is `{chandlerBase}/assets/{assetKey}/poster.jpg`,
    `/assets/{assetKey}/sprite.jpg`, `/assets/{assetKey}/sprite.vtt` — Chandler
    serves the object key directly, no version resolution. assetKey is stream_id
    for live streams; clip_hash / dvr_hash / vod_hash (= artifact_hash) for artifacts."""

    poster_url: str = Field(alias="posterUrl")
    sprite_vtt_url: str = Field(alias="spriteVttUrl")
    sprite_jpg_url: str = Field(alias="spriteJpgUrl")
    asset_key: str = Field(alias="assetKey")


class TrackListEventInNodeDefaultStreamRetentionOverrides(BaseModel):
    """Per-stream retention overrides for DVR and clips. VOD uploads aren't
    stream-bound, so they have no stream-level knob here."""

    stream_id: str = Field(alias="streamId")
    dvr_retention_days_override: Optional[int] = Field(
        alias="dvrRetentionDaysOverride",
        description="Null = no override (inherit tenant default). 0 = no auto-expire.",
    )
    "Null = no override (inherit tenant default). 0 = no auto-expire."
    clip_retention_days_override: Optional[int] = Field(
        alias="clipRetentionDaysOverride"
    )


class TrackListEventInNodeDefaultTracks(BaseModel):
    track_name: str = Field(alias="trackName")
    track_type: str = Field(alias="trackType")
    codec: Optional[str]
    bitrate_kbps: Optional[int] = Field(alias="bitrateKbps")
    bitrate_bps: Optional[int] = Field(alias="bitrateBps")
    buffer: Optional[int]
    jitter: Optional[int]
    width: Optional[int]
    height: Optional[int]
    fps: Optional[float]
    resolution: Optional[str]
    has_b_frames: Optional[bool] = Field(alias="hasBFrames")
    channels: Optional[int]
    sample_rate: Optional[int] = Field(alias="sampleRate")


class TrackListUpdateDefault(BaseModel):
    stream_id: str = Field(alias="streamId")
    stream: Optional["TrackListUpdateDefaultStream"]
    tracks: Optional[list["TrackListUpdateDefaultTracks"]]
    total_tracks: Optional[int] = Field(alias="totalTracks")
    video_track_count: Optional[int] = Field(alias="videoTrackCount")
    audio_track_count: Optional[int] = Field(alias="audioTrackCount")
    quality_tier: Optional[str] = Field(alias="qualityTier")
    primary_width: Optional[int] = Field(alias="primaryWidth")
    primary_height: Optional[int] = Field(alias="primaryHeight")
    primary_fps: Optional[float] = Field(alias="primaryFps")
    primary_video_bitrate: Optional[int] = Field(alias="primaryVideoBitrate")
    primary_video_codec: Optional[str] = Field(alias="primaryVideoCodec")
    primary_audio_bitrate: Optional[int] = Field(alias="primaryAudioBitrate")
    primary_audio_codec: Optional[str] = Field(alias="primaryAudioCodec")
    primary_audio_channels: Optional[int] = Field(alias="primaryAudioChannels")
    primary_audio_sample_rate: Optional[int] = Field(alias="primaryAudioSampleRate")


class TrackListUpdateDefaultStream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

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
        description="Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path.",
    )
    "Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path."
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
    pull_source: Optional["TrackListUpdateDefaultStreamPullSource"] = Field(
        alias="pullSource",
        description="Pull-source config for pull streams; null for push streams.",
    )
    "Pull-source config for pull streams; null for push streams."
    managed_source: Optional["TrackListUpdateDefaultStreamManagedSource"] = Field(
        alias="managedSource",
        description="Safe source summary for managed streams; null for push and pull streams.",
    )
    "Safe source summary for managed streams; null for push and pull streams."
    source_location: "TrackListUpdateDefaultStreamSourceLocation" = Field(
        alias="sourceLocation",
        description="Where the stream's source may be ingested, derived from the stream's own ingest placement rules.",
    )
    "Where the stream's source may be ingested, derived from the stream's own ingest placement rules."
    created_at: datetime = Field(
        alias="createdAt", description="When this stream was created."
    )
    "When this stream was created."
    updated_at: datetime = Field(
        alias="updatedAt", description="When this stream was last modified."
    )
    "When this stream was last modified."
    metrics: Optional["TrackListUpdateDefaultStreamMetrics"] = Field(
        description="Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    )
    "Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    push_targets: list["TrackListUpdateDefaultStreamPushTargets"] = Field(
        alias="pushTargets",
        description="Configured multistream push targets for this stream.",
    )
    "Configured multistream push targets for this stream."
    playback_policy: Optional["TrackListUpdateDefaultStreamPlaybackPolicy"] = Field(
        alias="playbackPolicy",
        description="Playback access policy. null/PUBLIC = anyone with the playbackId can watch.",
    )
    "Playback access policy. null/PUBLIC = anyone with the playbackId can watch."
    dvr_chapter_mode: Optional[DVRChapterMode] = Field(
        alias="dvrChapterMode",
        description="How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast.",
    )
    "How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast."
    dvr_chapter_interval_seconds: Optional[int] = Field(
        alias="dvrChapterIntervalSeconds",
        description="Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour).",
    )
    "Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour)."
    monitoring: MonitoringToggle = Field(
        description="Per-stream Skipper monitoring override (INHERIT follows tier)."
    )
    "Per-stream Skipper monitoring override (INHERIT follows tier)."
    thumbnail_assets: Optional["TrackListUpdateDefaultStreamThumbnailAssets"] = Field(
        alias="thumbnailAssets",
        description="Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles.",
    )
    "Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles."
    retention_overrides: Optional["TrackListUpdateDefaultStreamRetentionOverrides"] = (
        Field(
            alias="retentionOverrides",
            description="Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here.",
        )
    )
    "Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here."


class TrackListUpdateDefaultStreamPullSource(BaseModel):
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


class TrackListUpdateDefaultStreamManagedSource(BaseModel):
    """Safe summary of an operator-managed source. Literal paths, commands, and
    credentials are intentionally not exposed through the tenant API."""

    source_kind: str = Field(
        alias="sourceKind", description="Managed source form: file, playlist, or exec."
    )
    "Managed source form: file, playlist, or exec."
    always_on: bool = Field(
        alias="alwaysOn",
        description="Whether the source is kept active without waiting for a viewer.",
    )
    "Whether the source is kept active without waiting for a viewer."
    placement_count: int = Field(
        alias="placementCount",
        description="Number of source placements requested by the operator configuration.",
    )
    "Number of source placements requested by the operator configuration."


class TrackListUpdateDefaultStreamSourceLocation(BaseModel):
    mode: SourceLocationMode
    avoid_node_ids: list[str] = Field(
        alias="avoidNodeIds", description="Empty unless mode is RESTRICTED."
    )
    "Empty unless mode is RESTRICTED."


class TrackListUpdateDefaultStreamMetrics(BaseModel):
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
    node_id: Optional[str] = Field(
        alias="nodeId", description="ID of the edge node handling this stream."
    )
    "ID of the edge node handling this stream."
    track_count: Optional[int] = Field(
        alias="trackCount", description="Number of quality tracks available."
    )
    "Number of quality tracks available."
    total_inputs: Optional[int] = Field(
        alias="totalInputs", description="Total ingest connections to this stream."
    )
    "Total ingest connections to this stream."
    uploaded_bytes: float = Field(
        alias="uploadedBytes",
        description="Total bytes uploaded (ingested) for current session.",
    )
    "Total bytes uploaded (ingested) for current session."
    downloaded_bytes: float = Field(
        alias="downloadedBytes",
        description="Total bytes downloaded (egress) for current session.",
    )
    "Total bytes downloaded (egress) for current session."
    viewer_seconds: float = Field(
        alias="viewerSeconds", description="Total viewer-seconds accumulated."
    )
    "Total viewer-seconds accumulated."
    packets_sent: Optional[float] = Field(
        alias="packetsSent", description="Total packets sent to viewers."
    )
    "Total packets sent to viewers."
    packets_lost: Optional[float] = Field(
        alias="packetsLost", description="Total packets lost in transit."
    )
    "Total packets lost in transit."
    packets_retransmitted: Optional[float] = Field(
        alias="packetsRetransmitted", description="Total packets retransmitted."
    )
    "Total packets retransmitted."
    buffer_state: Optional[str] = Field(
        alias="bufferState",
        description="Buffer health state (HEALTHY, WARNING, CRITICAL).",
    )
    "Buffer health state (HEALTHY, WARNING, CRITICAL)."
    quality_tier: Optional[str] = Field(
        alias="qualityTier",
        description="Highest quality tier available (4K, 1080p, 720p, etc.).",
    )
    "Highest quality tier available (4K, 1080p, 720p, etc.)."
    primary_width: Optional[int] = Field(
        alias="primaryWidth", description="Primary video track width in pixels."
    )
    "Primary video track width in pixels."
    primary_height: Optional[int] = Field(
        alias="primaryHeight", description="Primary video track height in pixels."
    )
    "Primary video track height in pixels."
    primary_fps: Optional[float] = Field(
        alias="primaryFps", description="Primary video track framerate."
    )
    "Primary video track framerate."
    primary_codec: Optional[str] = Field(
        alias="primaryCodec",
        description="Primary video codec (H.264, H.265, VP9, AV1).",
    )
    "Primary video codec (H.264, H.265, VP9, AV1)."
    primary_bitrate: Optional[int] = Field(
        alias="primaryBitrate", description="Primary video bitrate in kbps."
    )
    "Primary video bitrate in kbps."
    has_issues: Optional[bool] = Field(
        alias="hasIssues", description="Whether the stream has active quality issues."
    )
    "Whether the stream has active quality issues."
    issues_description: Optional[str] = Field(
        alias="issuesDescription",
        description="Human-readable description of current issues.",
    )
    "Human-readable description of current issues."


class TrackListUpdateDefaultStreamPushTargets(BaseModel):
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


class TrackListUpdateDefaultStreamPlaybackPolicy(BaseModel):
    """Per-playback-object access policy. Foghorn reads this in the USER_NEW
    trigger handler; the requiresAuth marker (on the playback object itself)
    gates whether the full policy is fetched at all."""

    type_: PlaybackPolicyType = Field(alias="type")
    allowed_origins: list[str] = Field(
        alias="allowedOrigins",
        description="Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied.",
    )
    "Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied."


class TrackListUpdateDefaultStreamThumbnailAssets(BaseModel):
    """Chandler-served thumbnail asset URLs (poster, sprite, VTT cues). URL shape
    is `{chandlerBase}/assets/{assetKey}/poster.jpg`,
    `/assets/{assetKey}/sprite.jpg`, `/assets/{assetKey}/sprite.vtt` — Chandler
    serves the object key directly, no version resolution. assetKey is stream_id
    for live streams; clip_hash / dvr_hash / vod_hash (= artifact_hash) for artifacts."""

    poster_url: str = Field(alias="posterUrl")
    sprite_vtt_url: str = Field(alias="spriteVttUrl")
    sprite_jpg_url: str = Field(alias="spriteJpgUrl")
    asset_key: str = Field(alias="assetKey")


class TrackListUpdateDefaultStreamRetentionOverrides(BaseModel):
    """Per-stream retention overrides for DVR and clips. VOD uploads aren't
    stream-bound, so they have no stream-level knob here."""

    stream_id: str = Field(alias="streamId")
    dvr_retention_days_override: Optional[int] = Field(
        alias="dvrRetentionDaysOverride",
        description="Null = no override (inherit tenant default). 0 = no auto-expire.",
    )
    "Null = no override (inherit tenant default). 0 = no auto-expire."
    clip_retention_days_override: Optional[int] = Field(
        alias="clipRetentionDaysOverride"
    )


class TrackListUpdateDefaultTracks(BaseModel):
    track_name: str = Field(alias="trackName")
    track_type: str = Field(alias="trackType")
    codec: Optional[str]
    bitrate_kbps: Optional[int] = Field(alias="bitrateKbps")
    bitrate_bps: Optional[int] = Field(alias="bitrateBps")
    buffer: Optional[int]
    jitter: Optional[int]
    width: Optional[int]
    height: Optional[int]
    fps: Optional[float]
    resolution: Optional[str]
    has_b_frames: Optional[bool] = Field(alias="hasBFrames")
    channels: Optional[int]
    sample_rate: Optional[int] = Field(alias="sampleRate")


class ValidationErrorDefault(BaseModel):
    message: str
    code: Optional[str]
    field: Optional[str]
    constraint: Optional[str]


class ValidationError(BaseModel):
    typename__: str = Field(alias="__typename")
    message: str
    code: Optional[str]
    field: Optional[str]
    constraint: Optional[str]


class ViewerCountBucketDefault(BaseModel):
    timestamp: datetime
    viewer_count: int = Field(alias="viewerCount")
    stream_id: Optional[str] = Field(alias="streamId")
    stream: Optional["ViewerCountBucketDefaultStream"]


class ViewerCountBucketDefaultStream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

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
        description="Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path.",
    )
    "Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path."
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
    pull_source: Optional["ViewerCountBucketDefaultStreamPullSource"] = Field(
        alias="pullSource",
        description="Pull-source config for pull streams; null for push streams.",
    )
    "Pull-source config for pull streams; null for push streams."
    managed_source: Optional["ViewerCountBucketDefaultStreamManagedSource"] = Field(
        alias="managedSource",
        description="Safe source summary for managed streams; null for push and pull streams.",
    )
    "Safe source summary for managed streams; null for push and pull streams."
    source_location: "ViewerCountBucketDefaultStreamSourceLocation" = Field(
        alias="sourceLocation",
        description="Where the stream's source may be ingested, derived from the stream's own ingest placement rules.",
    )
    "Where the stream's source may be ingested, derived from the stream's own ingest placement rules."
    created_at: datetime = Field(
        alias="createdAt", description="When this stream was created."
    )
    "When this stream was created."
    updated_at: datetime = Field(
        alias="updatedAt", description="When this stream was last modified."
    )
    "When this stream was last modified."
    metrics: Optional["ViewerCountBucketDefaultStreamMetrics"] = Field(
        description="Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    )
    "Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    push_targets: list["ViewerCountBucketDefaultStreamPushTargets"] = Field(
        alias="pushTargets",
        description="Configured multistream push targets for this stream.",
    )
    "Configured multistream push targets for this stream."
    playback_policy: Optional["ViewerCountBucketDefaultStreamPlaybackPolicy"] = Field(
        alias="playbackPolicy",
        description="Playback access policy. null/PUBLIC = anyone with the playbackId can watch.",
    )
    "Playback access policy. null/PUBLIC = anyone with the playbackId can watch."
    dvr_chapter_mode: Optional[DVRChapterMode] = Field(
        alias="dvrChapterMode",
        description="How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast.",
    )
    "How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast."
    dvr_chapter_interval_seconds: Optional[int] = Field(
        alias="dvrChapterIntervalSeconds",
        description="Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour).",
    )
    "Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour)."
    monitoring: MonitoringToggle = Field(
        description="Per-stream Skipper monitoring override (INHERIT follows tier)."
    )
    "Per-stream Skipper monitoring override (INHERIT follows tier)."
    thumbnail_assets: Optional["ViewerCountBucketDefaultStreamThumbnailAssets"] = Field(
        alias="thumbnailAssets",
        description="Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles.",
    )
    "Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles."
    retention_overrides: Optional[
        "ViewerCountBucketDefaultStreamRetentionOverrides"
    ] = Field(
        alias="retentionOverrides",
        description="Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here.",
    )
    "Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here."


class ViewerCountBucketDefaultStreamPullSource(BaseModel):
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


class ViewerCountBucketDefaultStreamManagedSource(BaseModel):
    """Safe summary of an operator-managed source. Literal paths, commands, and
    credentials are intentionally not exposed through the tenant API."""

    source_kind: str = Field(
        alias="sourceKind", description="Managed source form: file, playlist, or exec."
    )
    "Managed source form: file, playlist, or exec."
    always_on: bool = Field(
        alias="alwaysOn",
        description="Whether the source is kept active without waiting for a viewer.",
    )
    "Whether the source is kept active without waiting for a viewer."
    placement_count: int = Field(
        alias="placementCount",
        description="Number of source placements requested by the operator configuration.",
    )
    "Number of source placements requested by the operator configuration."


class ViewerCountBucketDefaultStreamSourceLocation(BaseModel):
    mode: SourceLocationMode
    avoid_node_ids: list[str] = Field(
        alias="avoidNodeIds", description="Empty unless mode is RESTRICTED."
    )
    "Empty unless mode is RESTRICTED."


class ViewerCountBucketDefaultStreamMetrics(BaseModel):
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
    node_id: Optional[str] = Field(
        alias="nodeId", description="ID of the edge node handling this stream."
    )
    "ID of the edge node handling this stream."
    track_count: Optional[int] = Field(
        alias="trackCount", description="Number of quality tracks available."
    )
    "Number of quality tracks available."
    total_inputs: Optional[int] = Field(
        alias="totalInputs", description="Total ingest connections to this stream."
    )
    "Total ingest connections to this stream."
    uploaded_bytes: float = Field(
        alias="uploadedBytes",
        description="Total bytes uploaded (ingested) for current session.",
    )
    "Total bytes uploaded (ingested) for current session."
    downloaded_bytes: float = Field(
        alias="downloadedBytes",
        description="Total bytes downloaded (egress) for current session.",
    )
    "Total bytes downloaded (egress) for current session."
    viewer_seconds: float = Field(
        alias="viewerSeconds", description="Total viewer-seconds accumulated."
    )
    "Total viewer-seconds accumulated."
    packets_sent: Optional[float] = Field(
        alias="packetsSent", description="Total packets sent to viewers."
    )
    "Total packets sent to viewers."
    packets_lost: Optional[float] = Field(
        alias="packetsLost", description="Total packets lost in transit."
    )
    "Total packets lost in transit."
    packets_retransmitted: Optional[float] = Field(
        alias="packetsRetransmitted", description="Total packets retransmitted."
    )
    "Total packets retransmitted."
    buffer_state: Optional[str] = Field(
        alias="bufferState",
        description="Buffer health state (HEALTHY, WARNING, CRITICAL).",
    )
    "Buffer health state (HEALTHY, WARNING, CRITICAL)."
    quality_tier: Optional[str] = Field(
        alias="qualityTier",
        description="Highest quality tier available (4K, 1080p, 720p, etc.).",
    )
    "Highest quality tier available (4K, 1080p, 720p, etc.)."
    primary_width: Optional[int] = Field(
        alias="primaryWidth", description="Primary video track width in pixels."
    )
    "Primary video track width in pixels."
    primary_height: Optional[int] = Field(
        alias="primaryHeight", description="Primary video track height in pixels."
    )
    "Primary video track height in pixels."
    primary_fps: Optional[float] = Field(
        alias="primaryFps", description="Primary video track framerate."
    )
    "Primary video track framerate."
    primary_codec: Optional[str] = Field(
        alias="primaryCodec",
        description="Primary video codec (H.264, H.265, VP9, AV1).",
    )
    "Primary video codec (H.264, H.265, VP9, AV1)."
    primary_bitrate: Optional[int] = Field(
        alias="primaryBitrate", description="Primary video bitrate in kbps."
    )
    "Primary video bitrate in kbps."
    has_issues: Optional[bool] = Field(
        alias="hasIssues", description="Whether the stream has active quality issues."
    )
    "Whether the stream has active quality issues."
    issues_description: Optional[str] = Field(
        alias="issuesDescription",
        description="Human-readable description of current issues.",
    )
    "Human-readable description of current issues."


class ViewerCountBucketDefaultStreamPushTargets(BaseModel):
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


class ViewerCountBucketDefaultStreamPlaybackPolicy(BaseModel):
    """Per-playback-object access policy. Foghorn reads this in the USER_NEW
    trigger handler; the requiresAuth marker (on the playback object itself)
    gates whether the full policy is fetched at all."""

    type_: PlaybackPolicyType = Field(alias="type")
    allowed_origins: list[str] = Field(
        alias="allowedOrigins",
        description="Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied.",
    )
    "Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied."


class ViewerCountBucketDefaultStreamThumbnailAssets(BaseModel):
    """Chandler-served thumbnail asset URLs (poster, sprite, VTT cues). URL shape
    is `{chandlerBase}/assets/{assetKey}/poster.jpg`,
    `/assets/{assetKey}/sprite.jpg`, `/assets/{assetKey}/sprite.vtt` — Chandler
    serves the object key directly, no version resolution. assetKey is stream_id
    for live streams; clip_hash / dvr_hash / vod_hash (= artifact_hash) for artifacts."""

    poster_url: str = Field(alias="posterUrl")
    sprite_vtt_url: str = Field(alias="spriteVttUrl")
    sprite_jpg_url: str = Field(alias="spriteJpgUrl")
    asset_key: str = Field(alias="assetKey")


class ViewerCountBucketDefaultStreamRetentionOverrides(BaseModel):
    """Per-stream retention overrides for DVR and clips. VOD uploads aren't
    stream-bound, so they have no stream-level knob here."""

    stream_id: str = Field(alias="streamId")
    dvr_retention_days_override: Optional[int] = Field(
        alias="dvrRetentionDaysOverride",
        description="Null = no override (inherit tenant default). 0 = no auto-expire.",
    )
    "Null = no override (inherit tenant default). 0 = no auto-expire."
    clip_retention_days_override: Optional[int] = Field(
        alias="clipRetentionDaysOverride"
    )


class ViewerEndpoint(BaseModel):
    node_id: str = Field(alias="nodeId")
    base_url: str = Field(alias="baseUrl")
    protocol: str
    url: str
    geo_distance: Optional[float] = Field(alias="geoDistance")
    load_score: Optional[float] = Field(alias="loadScore")
    outputs: Optional[Any]


class ViewerGeoHourlyDefault(BaseModel):
    id: str
    hour: datetime
    country_code: str = Field(alias="countryCode")
    viewer_count: int = Field(alias="viewerCount")
    viewer_hours: float = Field(alias="viewerHours")
    egress_gb: float = Field(alias="egressGb")


class ViewerGeoHourlyInNodeDefault(BaseModel):
    id: str
    hour: datetime
    viewer_geo_hourly_country_code: str = Field(alias="viewerGeoHourlyCountryCode")
    viewer_count: int = Field(alias="viewerCount")
    viewer_hours: float = Field(alias="viewerHours")
    egress_gb: float = Field(alias="egressGb")


class ViewerGeographicDefault(BaseModel):
    timestamp: datetime
    stream_id: Optional[str] = Field(alias="streamId")
    stream: Optional["ViewerGeographicDefaultStream"]
    node_id: Optional[str] = Field(alias="nodeId")
    country_code: Optional[str] = Field(alias="countryCode")
    city: Optional[str]
    latitude: Optional[float]
    longitude: Optional[float]
    viewer_count: Optional[int] = Field(alias="viewerCount")
    connection_addr: Optional[str] = Field(alias="connectionAddr")
    event_type: Optional[str] = Field(alias="eventType")
    source: Optional[str]
    session_duration_seconds: Optional[int] = Field(alias="sessionDurationSeconds")
    bytes_transferred: Optional[float] = Field(alias="bytesTransferred")
    connector: Optional[str]


class ViewerGeographicDefaultStream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

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
        description="Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path.",
    )
    "Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path."
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
    pull_source: Optional["ViewerGeographicDefaultStreamPullSource"] = Field(
        alias="pullSource",
        description="Pull-source config for pull streams; null for push streams.",
    )
    "Pull-source config for pull streams; null for push streams."
    managed_source: Optional["ViewerGeographicDefaultStreamManagedSource"] = Field(
        alias="managedSource",
        description="Safe source summary for managed streams; null for push and pull streams.",
    )
    "Safe source summary for managed streams; null for push and pull streams."
    source_location: "ViewerGeographicDefaultStreamSourceLocation" = Field(
        alias="sourceLocation",
        description="Where the stream's source may be ingested, derived from the stream's own ingest placement rules.",
    )
    "Where the stream's source may be ingested, derived from the stream's own ingest placement rules."
    created_at: datetime = Field(
        alias="createdAt", description="When this stream was created."
    )
    "When this stream was created."
    updated_at: datetime = Field(
        alias="updatedAt", description="When this stream was last modified."
    )
    "When this stream was last modified."
    metrics: Optional["ViewerGeographicDefaultStreamMetrics"] = Field(
        description="Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    )
    "Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    push_targets: list["ViewerGeographicDefaultStreamPushTargets"] = Field(
        alias="pushTargets",
        description="Configured multistream push targets for this stream.",
    )
    "Configured multistream push targets for this stream."
    playback_policy: Optional["ViewerGeographicDefaultStreamPlaybackPolicy"] = Field(
        alias="playbackPolicy",
        description="Playback access policy. null/PUBLIC = anyone with the playbackId can watch.",
    )
    "Playback access policy. null/PUBLIC = anyone with the playbackId can watch."
    dvr_chapter_mode: Optional[DVRChapterMode] = Field(
        alias="dvrChapterMode",
        description="How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast.",
    )
    "How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast."
    dvr_chapter_interval_seconds: Optional[int] = Field(
        alias="dvrChapterIntervalSeconds",
        description="Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour).",
    )
    "Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour)."
    monitoring: MonitoringToggle = Field(
        description="Per-stream Skipper monitoring override (INHERIT follows tier)."
    )
    "Per-stream Skipper monitoring override (INHERIT follows tier)."
    thumbnail_assets: Optional["ViewerGeographicDefaultStreamThumbnailAssets"] = Field(
        alias="thumbnailAssets",
        description="Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles.",
    )
    "Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles."
    retention_overrides: Optional["ViewerGeographicDefaultStreamRetentionOverrides"] = (
        Field(
            alias="retentionOverrides",
            description="Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here.",
        )
    )
    "Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here."


class ViewerGeographicDefaultStreamPullSource(BaseModel):
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


class ViewerGeographicDefaultStreamManagedSource(BaseModel):
    """Safe summary of an operator-managed source. Literal paths, commands, and
    credentials are intentionally not exposed through the tenant API."""

    source_kind: str = Field(
        alias="sourceKind", description="Managed source form: file, playlist, or exec."
    )
    "Managed source form: file, playlist, or exec."
    always_on: bool = Field(
        alias="alwaysOn",
        description="Whether the source is kept active without waiting for a viewer.",
    )
    "Whether the source is kept active without waiting for a viewer."
    placement_count: int = Field(
        alias="placementCount",
        description="Number of source placements requested by the operator configuration.",
    )
    "Number of source placements requested by the operator configuration."


class ViewerGeographicDefaultStreamSourceLocation(BaseModel):
    mode: SourceLocationMode
    avoid_node_ids: list[str] = Field(
        alias="avoidNodeIds", description="Empty unless mode is RESTRICTED."
    )
    "Empty unless mode is RESTRICTED."


class ViewerGeographicDefaultStreamMetrics(BaseModel):
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
    node_id: Optional[str] = Field(
        alias="nodeId", description="ID of the edge node handling this stream."
    )
    "ID of the edge node handling this stream."
    track_count: Optional[int] = Field(
        alias="trackCount", description="Number of quality tracks available."
    )
    "Number of quality tracks available."
    total_inputs: Optional[int] = Field(
        alias="totalInputs", description="Total ingest connections to this stream."
    )
    "Total ingest connections to this stream."
    uploaded_bytes: float = Field(
        alias="uploadedBytes",
        description="Total bytes uploaded (ingested) for current session.",
    )
    "Total bytes uploaded (ingested) for current session."
    downloaded_bytes: float = Field(
        alias="downloadedBytes",
        description="Total bytes downloaded (egress) for current session.",
    )
    "Total bytes downloaded (egress) for current session."
    viewer_seconds: float = Field(
        alias="viewerSeconds", description="Total viewer-seconds accumulated."
    )
    "Total viewer-seconds accumulated."
    packets_sent: Optional[float] = Field(
        alias="packetsSent", description="Total packets sent to viewers."
    )
    "Total packets sent to viewers."
    packets_lost: Optional[float] = Field(
        alias="packetsLost", description="Total packets lost in transit."
    )
    "Total packets lost in transit."
    packets_retransmitted: Optional[float] = Field(
        alias="packetsRetransmitted", description="Total packets retransmitted."
    )
    "Total packets retransmitted."
    buffer_state: Optional[str] = Field(
        alias="bufferState",
        description="Buffer health state (HEALTHY, WARNING, CRITICAL).",
    )
    "Buffer health state (HEALTHY, WARNING, CRITICAL)."
    quality_tier: Optional[str] = Field(
        alias="qualityTier",
        description="Highest quality tier available (4K, 1080p, 720p, etc.).",
    )
    "Highest quality tier available (4K, 1080p, 720p, etc.)."
    primary_width: Optional[int] = Field(
        alias="primaryWidth", description="Primary video track width in pixels."
    )
    "Primary video track width in pixels."
    primary_height: Optional[int] = Field(
        alias="primaryHeight", description="Primary video track height in pixels."
    )
    "Primary video track height in pixels."
    primary_fps: Optional[float] = Field(
        alias="primaryFps", description="Primary video track framerate."
    )
    "Primary video track framerate."
    primary_codec: Optional[str] = Field(
        alias="primaryCodec",
        description="Primary video codec (H.264, H.265, VP9, AV1).",
    )
    "Primary video codec (H.264, H.265, VP9, AV1)."
    primary_bitrate: Optional[int] = Field(
        alias="primaryBitrate", description="Primary video bitrate in kbps."
    )
    "Primary video bitrate in kbps."
    has_issues: Optional[bool] = Field(
        alias="hasIssues", description="Whether the stream has active quality issues."
    )
    "Whether the stream has active quality issues."
    issues_description: Optional[str] = Field(
        alias="issuesDescription",
        description="Human-readable description of current issues.",
    )
    "Human-readable description of current issues."


class ViewerGeographicDefaultStreamPushTargets(BaseModel):
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


class ViewerGeographicDefaultStreamPlaybackPolicy(BaseModel):
    """Per-playback-object access policy. Foghorn reads this in the USER_NEW
    trigger handler; the requiresAuth marker (on the playback object itself)
    gates whether the full policy is fetched at all."""

    type_: PlaybackPolicyType = Field(alias="type")
    allowed_origins: list[str] = Field(
        alias="allowedOrigins",
        description="Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied.",
    )
    "Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied."


class ViewerGeographicDefaultStreamThumbnailAssets(BaseModel):
    """Chandler-served thumbnail asset URLs (poster, sprite, VTT cues). URL shape
    is `{chandlerBase}/assets/{assetKey}/poster.jpg`,
    `/assets/{assetKey}/sprite.jpg`, `/assets/{assetKey}/sprite.vtt` — Chandler
    serves the object key directly, no version resolution. assetKey is stream_id
    for live streams; clip_hash / dvr_hash / vod_hash (= artifact_hash) for artifacts."""

    poster_url: str = Field(alias="posterUrl")
    sprite_vtt_url: str = Field(alias="spriteVttUrl")
    sprite_jpg_url: str = Field(alias="spriteJpgUrl")
    asset_key: str = Field(alias="assetKey")


class ViewerGeographicDefaultStreamRetentionOverrides(BaseModel):
    """Per-stream retention overrides for DVR and clips. VOD uploads aren't
    stream-bound, so they have no stream-level knob here."""

    stream_id: str = Field(alias="streamId")
    dvr_retention_days_override: Optional[int] = Field(
        alias="dvrRetentionDaysOverride",
        description="Null = no override (inherit tenant default). 0 = no auto-expire.",
    )
    "Null = no override (inherit tenant default). 0 = no auto-expire."
    clip_retention_days_override: Optional[int] = Field(
        alias="clipRetentionDaysOverride"
    )


class ViewerHoursHourlyDefault(BaseModel):
    id: str
    hour: datetime
    stream_id: Optional[str] = Field(alias="streamId")
    stream: Optional["ViewerHoursHourlyDefaultStream"]
    country_code: Optional[str] = Field(alias="countryCode")
    unique_viewers: int = Field(alias="uniqueViewers")
    total_session_seconds: int = Field(alias="totalSessionSeconds")
    total_bytes: float = Field(alias="totalBytes")
    viewer_hours: float = Field(alias="viewerHours")
    egress_gb: float = Field(alias="egressGb")


class ViewerHoursHourlyDefaultStream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

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
        description="Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path.",
    )
    "Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path."
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
    pull_source: Optional["ViewerHoursHourlyDefaultStreamPullSource"] = Field(
        alias="pullSource",
        description="Pull-source config for pull streams; null for push streams.",
    )
    "Pull-source config for pull streams; null for push streams."
    managed_source: Optional["ViewerHoursHourlyDefaultStreamManagedSource"] = Field(
        alias="managedSource",
        description="Safe source summary for managed streams; null for push and pull streams.",
    )
    "Safe source summary for managed streams; null for push and pull streams."
    source_location: "ViewerHoursHourlyDefaultStreamSourceLocation" = Field(
        alias="sourceLocation",
        description="Where the stream's source may be ingested, derived from the stream's own ingest placement rules.",
    )
    "Where the stream's source may be ingested, derived from the stream's own ingest placement rules."
    created_at: datetime = Field(
        alias="createdAt", description="When this stream was created."
    )
    "When this stream was created."
    updated_at: datetime = Field(
        alias="updatedAt", description="When this stream was last modified."
    )
    "When this stream was last modified."
    metrics: Optional["ViewerHoursHourlyDefaultStreamMetrics"] = Field(
        description="Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    )
    "Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    push_targets: list["ViewerHoursHourlyDefaultStreamPushTargets"] = Field(
        alias="pushTargets",
        description="Configured multistream push targets for this stream.",
    )
    "Configured multistream push targets for this stream."
    playback_policy: Optional["ViewerHoursHourlyDefaultStreamPlaybackPolicy"] = Field(
        alias="playbackPolicy",
        description="Playback access policy. null/PUBLIC = anyone with the playbackId can watch.",
    )
    "Playback access policy. null/PUBLIC = anyone with the playbackId can watch."
    dvr_chapter_mode: Optional[DVRChapterMode] = Field(
        alias="dvrChapterMode",
        description="How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast.",
    )
    "How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast."
    dvr_chapter_interval_seconds: Optional[int] = Field(
        alias="dvrChapterIntervalSeconds",
        description="Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour).",
    )
    "Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour)."
    monitoring: MonitoringToggle = Field(
        description="Per-stream Skipper monitoring override (INHERIT follows tier)."
    )
    "Per-stream Skipper monitoring override (INHERIT follows tier)."
    thumbnail_assets: Optional["ViewerHoursHourlyDefaultStreamThumbnailAssets"] = Field(
        alias="thumbnailAssets",
        description="Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles.",
    )
    "Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles."
    retention_overrides: Optional[
        "ViewerHoursHourlyDefaultStreamRetentionOverrides"
    ] = Field(
        alias="retentionOverrides",
        description="Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here.",
    )
    "Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here."


class ViewerHoursHourlyDefaultStreamPullSource(BaseModel):
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


class ViewerHoursHourlyDefaultStreamManagedSource(BaseModel):
    """Safe summary of an operator-managed source. Literal paths, commands, and
    credentials are intentionally not exposed through the tenant API."""

    source_kind: str = Field(
        alias="sourceKind", description="Managed source form: file, playlist, or exec."
    )
    "Managed source form: file, playlist, or exec."
    always_on: bool = Field(
        alias="alwaysOn",
        description="Whether the source is kept active without waiting for a viewer.",
    )
    "Whether the source is kept active without waiting for a viewer."
    placement_count: int = Field(
        alias="placementCount",
        description="Number of source placements requested by the operator configuration.",
    )
    "Number of source placements requested by the operator configuration."


class ViewerHoursHourlyDefaultStreamSourceLocation(BaseModel):
    mode: SourceLocationMode
    avoid_node_ids: list[str] = Field(
        alias="avoidNodeIds", description="Empty unless mode is RESTRICTED."
    )
    "Empty unless mode is RESTRICTED."


class ViewerHoursHourlyDefaultStreamMetrics(BaseModel):
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
    node_id: Optional[str] = Field(
        alias="nodeId", description="ID of the edge node handling this stream."
    )
    "ID of the edge node handling this stream."
    track_count: Optional[int] = Field(
        alias="trackCount", description="Number of quality tracks available."
    )
    "Number of quality tracks available."
    total_inputs: Optional[int] = Field(
        alias="totalInputs", description="Total ingest connections to this stream."
    )
    "Total ingest connections to this stream."
    uploaded_bytes: float = Field(
        alias="uploadedBytes",
        description="Total bytes uploaded (ingested) for current session.",
    )
    "Total bytes uploaded (ingested) for current session."
    downloaded_bytes: float = Field(
        alias="downloadedBytes",
        description="Total bytes downloaded (egress) for current session.",
    )
    "Total bytes downloaded (egress) for current session."
    viewer_seconds: float = Field(
        alias="viewerSeconds", description="Total viewer-seconds accumulated."
    )
    "Total viewer-seconds accumulated."
    packets_sent: Optional[float] = Field(
        alias="packetsSent", description="Total packets sent to viewers."
    )
    "Total packets sent to viewers."
    packets_lost: Optional[float] = Field(
        alias="packetsLost", description="Total packets lost in transit."
    )
    "Total packets lost in transit."
    packets_retransmitted: Optional[float] = Field(
        alias="packetsRetransmitted", description="Total packets retransmitted."
    )
    "Total packets retransmitted."
    buffer_state: Optional[str] = Field(
        alias="bufferState",
        description="Buffer health state (HEALTHY, WARNING, CRITICAL).",
    )
    "Buffer health state (HEALTHY, WARNING, CRITICAL)."
    quality_tier: Optional[str] = Field(
        alias="qualityTier",
        description="Highest quality tier available (4K, 1080p, 720p, etc.).",
    )
    "Highest quality tier available (4K, 1080p, 720p, etc.)."
    primary_width: Optional[int] = Field(
        alias="primaryWidth", description="Primary video track width in pixels."
    )
    "Primary video track width in pixels."
    primary_height: Optional[int] = Field(
        alias="primaryHeight", description="Primary video track height in pixels."
    )
    "Primary video track height in pixels."
    primary_fps: Optional[float] = Field(
        alias="primaryFps", description="Primary video track framerate."
    )
    "Primary video track framerate."
    primary_codec: Optional[str] = Field(
        alias="primaryCodec",
        description="Primary video codec (H.264, H.265, VP9, AV1).",
    )
    "Primary video codec (H.264, H.265, VP9, AV1)."
    primary_bitrate: Optional[int] = Field(
        alias="primaryBitrate", description="Primary video bitrate in kbps."
    )
    "Primary video bitrate in kbps."
    has_issues: Optional[bool] = Field(
        alias="hasIssues", description="Whether the stream has active quality issues."
    )
    "Whether the stream has active quality issues."
    issues_description: Optional[str] = Field(
        alias="issuesDescription",
        description="Human-readable description of current issues.",
    )
    "Human-readable description of current issues."


class ViewerHoursHourlyDefaultStreamPushTargets(BaseModel):
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


class ViewerHoursHourlyDefaultStreamPlaybackPolicy(BaseModel):
    """Per-playback-object access policy. Foghorn reads this in the USER_NEW
    trigger handler; the requiresAuth marker (on the playback object itself)
    gates whether the full policy is fetched at all."""

    type_: PlaybackPolicyType = Field(alias="type")
    allowed_origins: list[str] = Field(
        alias="allowedOrigins",
        description="Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied.",
    )
    "Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied."


class ViewerHoursHourlyDefaultStreamThumbnailAssets(BaseModel):
    """Chandler-served thumbnail asset URLs (poster, sprite, VTT cues). URL shape
    is `{chandlerBase}/assets/{assetKey}/poster.jpg`,
    `/assets/{assetKey}/sprite.jpg`, `/assets/{assetKey}/sprite.vtt` — Chandler
    serves the object key directly, no version resolution. assetKey is stream_id
    for live streams; clip_hash / dvr_hash / vod_hash (= artifact_hash) for artifacts."""

    poster_url: str = Field(alias="posterUrl")
    sprite_vtt_url: str = Field(alias="spriteVttUrl")
    sprite_jpg_url: str = Field(alias="spriteJpgUrl")
    asset_key: str = Field(alias="assetKey")


class ViewerHoursHourlyDefaultStreamRetentionOverrides(BaseModel):
    """Per-stream retention overrides for DVR and clips. VOD uploads aren't
    stream-bound, so they have no stream-level knob here."""

    stream_id: str = Field(alias="streamId")
    dvr_retention_days_override: Optional[int] = Field(
        alias="dvrRetentionDaysOverride",
        description="Null = no override (inherit tenant default). 0 = no auto-expire.",
    )
    "Null = no override (inherit tenant default). 0 = no auto-expire."
    clip_retention_days_override: Optional[int] = Field(
        alias="clipRetentionDaysOverride"
    )


class ViewerHoursHourlyInNodeDefault(BaseModel):
    id: str
    hour: datetime
    viewer_hours_hourly_stream_id: Optional[str] = Field(
        alias="viewerHoursHourlyStreamId"
    )
    stream: Optional["ViewerHoursHourlyInNodeDefaultStream"]
    country_code: Optional[str] = Field(alias="countryCode")
    unique_viewers: int = Field(alias="uniqueViewers")
    total_session_seconds: int = Field(alias="totalSessionSeconds")
    total_bytes: float = Field(alias="totalBytes")
    viewer_hours: float = Field(alias="viewerHours")
    egress_gb: float = Field(alias="egressGb")


class ViewerHoursHourlyInNodeDefaultStream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

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
        description="Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path.",
    )
    "Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path."
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
    pull_source: Optional["ViewerHoursHourlyInNodeDefaultStreamPullSource"] = Field(
        alias="pullSource",
        description="Pull-source config for pull streams; null for push streams.",
    )
    "Pull-source config for pull streams; null for push streams."
    managed_source: Optional["ViewerHoursHourlyInNodeDefaultStreamManagedSource"] = (
        Field(
            alias="managedSource",
            description="Safe source summary for managed streams; null for push and pull streams.",
        )
    )
    "Safe source summary for managed streams; null for push and pull streams."
    source_location: "ViewerHoursHourlyInNodeDefaultStreamSourceLocation" = Field(
        alias="sourceLocation",
        description="Where the stream's source may be ingested, derived from the stream's own ingest placement rules.",
    )
    "Where the stream's source may be ingested, derived from the stream's own ingest placement rules."
    created_at: datetime = Field(
        alias="createdAt", description="When this stream was created."
    )
    "When this stream was created."
    updated_at: datetime = Field(
        alias="updatedAt", description="When this stream was last modified."
    )
    "When this stream was last modified."
    metrics: Optional["ViewerHoursHourlyInNodeDefaultStreamMetrics"] = Field(
        description="Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    )
    "Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    push_targets: list["ViewerHoursHourlyInNodeDefaultStreamPushTargets"] = Field(
        alias="pushTargets",
        description="Configured multistream push targets for this stream.",
    )
    "Configured multistream push targets for this stream."
    playback_policy: Optional["ViewerHoursHourlyInNodeDefaultStreamPlaybackPolicy"] = (
        Field(
            alias="playbackPolicy",
            description="Playback access policy. null/PUBLIC = anyone with the playbackId can watch.",
        )
    )
    "Playback access policy. null/PUBLIC = anyone with the playbackId can watch."
    dvr_chapter_mode: Optional[DVRChapterMode] = Field(
        alias="dvrChapterMode",
        description="How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast.",
    )
    "How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast."
    dvr_chapter_interval_seconds: Optional[int] = Field(
        alias="dvrChapterIntervalSeconds",
        description="Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour).",
    )
    "Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour)."
    monitoring: MonitoringToggle = Field(
        description="Per-stream Skipper monitoring override (INHERIT follows tier)."
    )
    "Per-stream Skipper monitoring override (INHERIT follows tier)."
    thumbnail_assets: Optional[
        "ViewerHoursHourlyInNodeDefaultStreamThumbnailAssets"
    ] = Field(
        alias="thumbnailAssets",
        description="Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles.",
    )
    "Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles."
    retention_overrides: Optional[
        "ViewerHoursHourlyInNodeDefaultStreamRetentionOverrides"
    ] = Field(
        alias="retentionOverrides",
        description="Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here.",
    )
    "Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here."


class ViewerHoursHourlyInNodeDefaultStreamPullSource(BaseModel):
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


class ViewerHoursHourlyInNodeDefaultStreamManagedSource(BaseModel):
    """Safe summary of an operator-managed source. Literal paths, commands, and
    credentials are intentionally not exposed through the tenant API."""

    source_kind: str = Field(
        alias="sourceKind", description="Managed source form: file, playlist, or exec."
    )
    "Managed source form: file, playlist, or exec."
    always_on: bool = Field(
        alias="alwaysOn",
        description="Whether the source is kept active without waiting for a viewer.",
    )
    "Whether the source is kept active without waiting for a viewer."
    placement_count: int = Field(
        alias="placementCount",
        description="Number of source placements requested by the operator configuration.",
    )
    "Number of source placements requested by the operator configuration."


class ViewerHoursHourlyInNodeDefaultStreamSourceLocation(BaseModel):
    mode: SourceLocationMode
    avoid_node_ids: list[str] = Field(
        alias="avoidNodeIds", description="Empty unless mode is RESTRICTED."
    )
    "Empty unless mode is RESTRICTED."


class ViewerHoursHourlyInNodeDefaultStreamMetrics(BaseModel):
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
    node_id: Optional[str] = Field(
        alias="nodeId", description="ID of the edge node handling this stream."
    )
    "ID of the edge node handling this stream."
    track_count: Optional[int] = Field(
        alias="trackCount", description="Number of quality tracks available."
    )
    "Number of quality tracks available."
    total_inputs: Optional[int] = Field(
        alias="totalInputs", description="Total ingest connections to this stream."
    )
    "Total ingest connections to this stream."
    uploaded_bytes: float = Field(
        alias="uploadedBytes",
        description="Total bytes uploaded (ingested) for current session.",
    )
    "Total bytes uploaded (ingested) for current session."
    downloaded_bytes: float = Field(
        alias="downloadedBytes",
        description="Total bytes downloaded (egress) for current session.",
    )
    "Total bytes downloaded (egress) for current session."
    viewer_seconds: float = Field(
        alias="viewerSeconds", description="Total viewer-seconds accumulated."
    )
    "Total viewer-seconds accumulated."
    packets_sent: Optional[float] = Field(
        alias="packetsSent", description="Total packets sent to viewers."
    )
    "Total packets sent to viewers."
    packets_lost: Optional[float] = Field(
        alias="packetsLost", description="Total packets lost in transit."
    )
    "Total packets lost in transit."
    packets_retransmitted: Optional[float] = Field(
        alias="packetsRetransmitted", description="Total packets retransmitted."
    )
    "Total packets retransmitted."
    buffer_state: Optional[str] = Field(
        alias="bufferState",
        description="Buffer health state (HEALTHY, WARNING, CRITICAL).",
    )
    "Buffer health state (HEALTHY, WARNING, CRITICAL)."
    quality_tier: Optional[str] = Field(
        alias="qualityTier",
        description="Highest quality tier available (4K, 1080p, 720p, etc.).",
    )
    "Highest quality tier available (4K, 1080p, 720p, etc.)."
    primary_width: Optional[int] = Field(
        alias="primaryWidth", description="Primary video track width in pixels."
    )
    "Primary video track width in pixels."
    primary_height: Optional[int] = Field(
        alias="primaryHeight", description="Primary video track height in pixels."
    )
    "Primary video track height in pixels."
    primary_fps: Optional[float] = Field(
        alias="primaryFps", description="Primary video track framerate."
    )
    "Primary video track framerate."
    primary_codec: Optional[str] = Field(
        alias="primaryCodec",
        description="Primary video codec (H.264, H.265, VP9, AV1).",
    )
    "Primary video codec (H.264, H.265, VP9, AV1)."
    primary_bitrate: Optional[int] = Field(
        alias="primaryBitrate", description="Primary video bitrate in kbps."
    )
    "Primary video bitrate in kbps."
    has_issues: Optional[bool] = Field(
        alias="hasIssues", description="Whether the stream has active quality issues."
    )
    "Whether the stream has active quality issues."
    issues_description: Optional[str] = Field(
        alias="issuesDescription",
        description="Human-readable description of current issues.",
    )
    "Human-readable description of current issues."


class ViewerHoursHourlyInNodeDefaultStreamPushTargets(BaseModel):
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


class ViewerHoursHourlyInNodeDefaultStreamPlaybackPolicy(BaseModel):
    """Per-playback-object access policy. Foghorn reads this in the USER_NEW
    trigger handler; the requiresAuth marker (on the playback object itself)
    gates whether the full policy is fetched at all."""

    type_: PlaybackPolicyType = Field(alias="type")
    allowed_origins: list[str] = Field(
        alias="allowedOrigins",
        description="Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied.",
    )
    "Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied."


class ViewerHoursHourlyInNodeDefaultStreamThumbnailAssets(BaseModel):
    """Chandler-served thumbnail asset URLs (poster, sprite, VTT cues). URL shape
    is `{chandlerBase}/assets/{assetKey}/poster.jpg`,
    `/assets/{assetKey}/sprite.jpg`, `/assets/{assetKey}/sprite.vtt` — Chandler
    serves the object key directly, no version resolution. assetKey is stream_id
    for live streams; clip_hash / dvr_hash / vod_hash (= artifact_hash) for artifacts."""

    poster_url: str = Field(alias="posterUrl")
    sprite_vtt_url: str = Field(alias="spriteVttUrl")
    sprite_jpg_url: str = Field(alias="spriteJpgUrl")
    asset_key: str = Field(alias="assetKey")


class ViewerHoursHourlyInNodeDefaultStreamRetentionOverrides(BaseModel):
    """Per-stream retention overrides for DVR and clips. VOD uploads aren't
    stream-bound, so they have no stream-level knob here."""

    stream_id: str = Field(alias="streamId")
    dvr_retention_days_override: Optional[int] = Field(
        alias="dvrRetentionDaysOverride",
        description="Null = no override (inherit tenant default). 0 = no auto-expire.",
    )
    "Null = no override (inherit tenant default). 0 = no auto-expire."
    clip_retention_days_override: Optional[int] = Field(
        alias="clipRetentionDaysOverride"
    )


class ViewerMetricsDefault(BaseModel):
    node_id: str = Field(alias="nodeId")
    stream_id: str = Field(alias="streamId")
    stream: Optional["ViewerMetricsDefaultStream"]
    action: str
    protocol: str
    host: Optional[str]
    session_id: Optional[str] = Field(alias="sessionId")
    connection_time: Optional[float] = Field(alias="connectionTime")
    position: Optional[float]
    bandwidth_in_bps: Optional[int] = Field(alias="bandwidthInBps")
    bandwidth_out_bps: Optional[int] = Field(alias="bandwidthOutBps")
    bytes_downloaded: Optional[float] = Field(alias="bytesDownloaded")
    bytes_uploaded: Optional[float] = Field(alias="bytesUploaded")
    packets_sent: Optional[int] = Field(alias="packetsSent")
    packets_lost: Optional[int] = Field(alias="packetsLost")
    packets_retransmitted: Optional[int] = Field(alias="packetsRetransmitted")
    timestamp: int
    client_country: Optional[str] = Field(alias="clientCountry")
    client_city: Optional[str] = Field(alias="clientCity")
    client_latitude: Optional[float] = Field(alias="clientLatitude")
    client_longitude: Optional[float] = Field(alias="clientLongitude")


class ViewerMetricsDefaultStream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

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
        description="Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path.",
    )
    "Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path."
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
    pull_source: Optional["ViewerMetricsDefaultStreamPullSource"] = Field(
        alias="pullSource",
        description="Pull-source config for pull streams; null for push streams.",
    )
    "Pull-source config for pull streams; null for push streams."
    managed_source: Optional["ViewerMetricsDefaultStreamManagedSource"] = Field(
        alias="managedSource",
        description="Safe source summary for managed streams; null for push and pull streams.",
    )
    "Safe source summary for managed streams; null for push and pull streams."
    source_location: "ViewerMetricsDefaultStreamSourceLocation" = Field(
        alias="sourceLocation",
        description="Where the stream's source may be ingested, derived from the stream's own ingest placement rules.",
    )
    "Where the stream's source may be ingested, derived from the stream's own ingest placement rules."
    created_at: datetime = Field(
        alias="createdAt", description="When this stream was created."
    )
    "When this stream was created."
    updated_at: datetime = Field(
        alias="updatedAt", description="When this stream was last modified."
    )
    "When this stream was last modified."
    metrics: Optional["ViewerMetricsDefaultStreamMetrics"] = Field(
        description="Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    )
    "Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    push_targets: list["ViewerMetricsDefaultStreamPushTargets"] = Field(
        alias="pushTargets",
        description="Configured multistream push targets for this stream.",
    )
    "Configured multistream push targets for this stream."
    playback_policy: Optional["ViewerMetricsDefaultStreamPlaybackPolicy"] = Field(
        alias="playbackPolicy",
        description="Playback access policy. null/PUBLIC = anyone with the playbackId can watch.",
    )
    "Playback access policy. null/PUBLIC = anyone with the playbackId can watch."
    dvr_chapter_mode: Optional[DVRChapterMode] = Field(
        alias="dvrChapterMode",
        description="How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast.",
    )
    "How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast."
    dvr_chapter_interval_seconds: Optional[int] = Field(
        alias="dvrChapterIntervalSeconds",
        description="Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour).",
    )
    "Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour)."
    monitoring: MonitoringToggle = Field(
        description="Per-stream Skipper monitoring override (INHERIT follows tier)."
    )
    "Per-stream Skipper monitoring override (INHERIT follows tier)."
    thumbnail_assets: Optional["ViewerMetricsDefaultStreamThumbnailAssets"] = Field(
        alias="thumbnailAssets",
        description="Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles.",
    )
    "Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles."
    retention_overrides: Optional["ViewerMetricsDefaultStreamRetentionOverrides"] = (
        Field(
            alias="retentionOverrides",
            description="Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here.",
        )
    )
    "Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here."


class ViewerMetricsDefaultStreamPullSource(BaseModel):
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


class ViewerMetricsDefaultStreamManagedSource(BaseModel):
    """Safe summary of an operator-managed source. Literal paths, commands, and
    credentials are intentionally not exposed through the tenant API."""

    source_kind: str = Field(
        alias="sourceKind", description="Managed source form: file, playlist, or exec."
    )
    "Managed source form: file, playlist, or exec."
    always_on: bool = Field(
        alias="alwaysOn",
        description="Whether the source is kept active without waiting for a viewer.",
    )
    "Whether the source is kept active without waiting for a viewer."
    placement_count: int = Field(
        alias="placementCount",
        description="Number of source placements requested by the operator configuration.",
    )
    "Number of source placements requested by the operator configuration."


class ViewerMetricsDefaultStreamSourceLocation(BaseModel):
    mode: SourceLocationMode
    avoid_node_ids: list[str] = Field(
        alias="avoidNodeIds", description="Empty unless mode is RESTRICTED."
    )
    "Empty unless mode is RESTRICTED."


class ViewerMetricsDefaultStreamMetrics(BaseModel):
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
    node_id: Optional[str] = Field(
        alias="nodeId", description="ID of the edge node handling this stream."
    )
    "ID of the edge node handling this stream."
    track_count: Optional[int] = Field(
        alias="trackCount", description="Number of quality tracks available."
    )
    "Number of quality tracks available."
    total_inputs: Optional[int] = Field(
        alias="totalInputs", description="Total ingest connections to this stream."
    )
    "Total ingest connections to this stream."
    uploaded_bytes: float = Field(
        alias="uploadedBytes",
        description="Total bytes uploaded (ingested) for current session.",
    )
    "Total bytes uploaded (ingested) for current session."
    downloaded_bytes: float = Field(
        alias="downloadedBytes",
        description="Total bytes downloaded (egress) for current session.",
    )
    "Total bytes downloaded (egress) for current session."
    viewer_seconds: float = Field(
        alias="viewerSeconds", description="Total viewer-seconds accumulated."
    )
    "Total viewer-seconds accumulated."
    packets_sent: Optional[float] = Field(
        alias="packetsSent", description="Total packets sent to viewers."
    )
    "Total packets sent to viewers."
    packets_lost: Optional[float] = Field(
        alias="packetsLost", description="Total packets lost in transit."
    )
    "Total packets lost in transit."
    packets_retransmitted: Optional[float] = Field(
        alias="packetsRetransmitted", description="Total packets retransmitted."
    )
    "Total packets retransmitted."
    buffer_state: Optional[str] = Field(
        alias="bufferState",
        description="Buffer health state (HEALTHY, WARNING, CRITICAL).",
    )
    "Buffer health state (HEALTHY, WARNING, CRITICAL)."
    quality_tier: Optional[str] = Field(
        alias="qualityTier",
        description="Highest quality tier available (4K, 1080p, 720p, etc.).",
    )
    "Highest quality tier available (4K, 1080p, 720p, etc.)."
    primary_width: Optional[int] = Field(
        alias="primaryWidth", description="Primary video track width in pixels."
    )
    "Primary video track width in pixels."
    primary_height: Optional[int] = Field(
        alias="primaryHeight", description="Primary video track height in pixels."
    )
    "Primary video track height in pixels."
    primary_fps: Optional[float] = Field(
        alias="primaryFps", description="Primary video track framerate."
    )
    "Primary video track framerate."
    primary_codec: Optional[str] = Field(
        alias="primaryCodec",
        description="Primary video codec (H.264, H.265, VP9, AV1).",
    )
    "Primary video codec (H.264, H.265, VP9, AV1)."
    primary_bitrate: Optional[int] = Field(
        alias="primaryBitrate", description="Primary video bitrate in kbps."
    )
    "Primary video bitrate in kbps."
    has_issues: Optional[bool] = Field(
        alias="hasIssues", description="Whether the stream has active quality issues."
    )
    "Whether the stream has active quality issues."
    issues_description: Optional[str] = Field(
        alias="issuesDescription",
        description="Human-readable description of current issues.",
    )
    "Human-readable description of current issues."


class ViewerMetricsDefaultStreamPushTargets(BaseModel):
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


class ViewerMetricsDefaultStreamPlaybackPolicy(BaseModel):
    """Per-playback-object access policy. Foghorn reads this in the USER_NEW
    trigger handler; the requiresAuth marker (on the playback object itself)
    gates whether the full policy is fetched at all."""

    type_: PlaybackPolicyType = Field(alias="type")
    allowed_origins: list[str] = Field(
        alias="allowedOrigins",
        description="Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied.",
    )
    "Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied."


class ViewerMetricsDefaultStreamThumbnailAssets(BaseModel):
    """Chandler-served thumbnail asset URLs (poster, sprite, VTT cues). URL shape
    is `{chandlerBase}/assets/{assetKey}/poster.jpg`,
    `/assets/{assetKey}/sprite.jpg`, `/assets/{assetKey}/sprite.vtt` — Chandler
    serves the object key directly, no version resolution. assetKey is stream_id
    for live streams; clip_hash / dvr_hash / vod_hash (= artifact_hash) for artifacts."""

    poster_url: str = Field(alias="posterUrl")
    sprite_vtt_url: str = Field(alias="spriteVttUrl")
    sprite_jpg_url: str = Field(alias="spriteJpgUrl")
    asset_key: str = Field(alias="assetKey")


class ViewerMetricsDefaultStreamRetentionOverrides(BaseModel):
    """Per-stream retention overrides for DVR and clips. VOD uploads aren't
    stream-bound, so they have no stream-level knob here."""

    stream_id: str = Field(alias="streamId")
    dvr_retention_days_override: Optional[int] = Field(
        alias="dvrRetentionDaysOverride",
        description="Null = no override (inherit tenant default). 0 = no auto-expire.",
    )
    "Null = no override (inherit tenant default). 0 = no auto-expire."
    clip_retention_days_override: Optional[int] = Field(
        alias="clipRetentionDaysOverride"
    )


class ViewerSessionDefault(BaseModel):
    id: str
    timestamp: datetime
    stream_id: str = Field(alias="streamId")
    stream: Optional["ViewerSessionDefaultStream"]
    node_id: Optional[str] = Field(alias="nodeId")
    session_id: str = Field(alias="sessionId")
    connected_at: Optional[datetime] = Field(alias="connectedAt")
    disconnected_at: Optional[datetime] = Field(alias="disconnectedAt")
    connector: Optional[str]
    country_code: Optional[str] = Field(alias="countryCode")
    city: Optional[str]
    latitude: Optional[float]
    longitude: Optional[float]
    duration_seconds: Optional[int] = Field(alias="durationSeconds")
    bytes_up: Optional[float] = Field(alias="bytesUp")
    bytes_down: Optional[float] = Field(alias="bytesDown")
    connection_quality: Optional[float] = Field(alias="connectionQuality")
    buffer_health: Optional[float] = Field(alias="bufferHealth")
    client_bucket: Optional["ViewerSessionDefaultClientBucket"] = Field(
        alias="clientBucket"
    )


class ViewerSessionDefaultStream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

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
        description="Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path.",
    )
    "Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path."
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
    pull_source: Optional["ViewerSessionDefaultStreamPullSource"] = Field(
        alias="pullSource",
        description="Pull-source config for pull streams; null for push streams.",
    )
    "Pull-source config for pull streams; null for push streams."
    managed_source: Optional["ViewerSessionDefaultStreamManagedSource"] = Field(
        alias="managedSource",
        description="Safe source summary for managed streams; null for push and pull streams.",
    )
    "Safe source summary for managed streams; null for push and pull streams."
    source_location: "ViewerSessionDefaultStreamSourceLocation" = Field(
        alias="sourceLocation",
        description="Where the stream's source may be ingested, derived from the stream's own ingest placement rules.",
    )
    "Where the stream's source may be ingested, derived from the stream's own ingest placement rules."
    created_at: datetime = Field(
        alias="createdAt", description="When this stream was created."
    )
    "When this stream was created."
    updated_at: datetime = Field(
        alias="updatedAt", description="When this stream was last modified."
    )
    "When this stream was last modified."
    metrics: Optional["ViewerSessionDefaultStreamMetrics"] = Field(
        description="Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    )
    "Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    push_targets: list["ViewerSessionDefaultStreamPushTargets"] = Field(
        alias="pushTargets",
        description="Configured multistream push targets for this stream.",
    )
    "Configured multistream push targets for this stream."
    playback_policy: Optional["ViewerSessionDefaultStreamPlaybackPolicy"] = Field(
        alias="playbackPolicy",
        description="Playback access policy. null/PUBLIC = anyone with the playbackId can watch.",
    )
    "Playback access policy. null/PUBLIC = anyone with the playbackId can watch."
    dvr_chapter_mode: Optional[DVRChapterMode] = Field(
        alias="dvrChapterMode",
        description="How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast.",
    )
    "How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast."
    dvr_chapter_interval_seconds: Optional[int] = Field(
        alias="dvrChapterIntervalSeconds",
        description="Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour).",
    )
    "Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour)."
    monitoring: MonitoringToggle = Field(
        description="Per-stream Skipper monitoring override (INHERIT follows tier)."
    )
    "Per-stream Skipper monitoring override (INHERIT follows tier)."
    thumbnail_assets: Optional["ViewerSessionDefaultStreamThumbnailAssets"] = Field(
        alias="thumbnailAssets",
        description="Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles.",
    )
    "Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles."
    retention_overrides: Optional["ViewerSessionDefaultStreamRetentionOverrides"] = (
        Field(
            alias="retentionOverrides",
            description="Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here.",
        )
    )
    "Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here."


class ViewerSessionDefaultStreamPullSource(BaseModel):
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


class ViewerSessionDefaultStreamManagedSource(BaseModel):
    """Safe summary of an operator-managed source. Literal paths, commands, and
    credentials are intentionally not exposed through the tenant API."""

    source_kind: str = Field(
        alias="sourceKind", description="Managed source form: file, playlist, or exec."
    )
    "Managed source form: file, playlist, or exec."
    always_on: bool = Field(
        alias="alwaysOn",
        description="Whether the source is kept active without waiting for a viewer.",
    )
    "Whether the source is kept active without waiting for a viewer."
    placement_count: int = Field(
        alias="placementCount",
        description="Number of source placements requested by the operator configuration.",
    )
    "Number of source placements requested by the operator configuration."


class ViewerSessionDefaultStreamSourceLocation(BaseModel):
    mode: SourceLocationMode
    avoid_node_ids: list[str] = Field(
        alias="avoidNodeIds", description="Empty unless mode is RESTRICTED."
    )
    "Empty unless mode is RESTRICTED."


class ViewerSessionDefaultStreamMetrics(BaseModel):
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
    node_id: Optional[str] = Field(
        alias="nodeId", description="ID of the edge node handling this stream."
    )
    "ID of the edge node handling this stream."
    track_count: Optional[int] = Field(
        alias="trackCount", description="Number of quality tracks available."
    )
    "Number of quality tracks available."
    total_inputs: Optional[int] = Field(
        alias="totalInputs", description="Total ingest connections to this stream."
    )
    "Total ingest connections to this stream."
    uploaded_bytes: float = Field(
        alias="uploadedBytes",
        description="Total bytes uploaded (ingested) for current session.",
    )
    "Total bytes uploaded (ingested) for current session."
    downloaded_bytes: float = Field(
        alias="downloadedBytes",
        description="Total bytes downloaded (egress) for current session.",
    )
    "Total bytes downloaded (egress) for current session."
    viewer_seconds: float = Field(
        alias="viewerSeconds", description="Total viewer-seconds accumulated."
    )
    "Total viewer-seconds accumulated."
    packets_sent: Optional[float] = Field(
        alias="packetsSent", description="Total packets sent to viewers."
    )
    "Total packets sent to viewers."
    packets_lost: Optional[float] = Field(
        alias="packetsLost", description="Total packets lost in transit."
    )
    "Total packets lost in transit."
    packets_retransmitted: Optional[float] = Field(
        alias="packetsRetransmitted", description="Total packets retransmitted."
    )
    "Total packets retransmitted."
    buffer_state: Optional[str] = Field(
        alias="bufferState",
        description="Buffer health state (HEALTHY, WARNING, CRITICAL).",
    )
    "Buffer health state (HEALTHY, WARNING, CRITICAL)."
    quality_tier: Optional[str] = Field(
        alias="qualityTier",
        description="Highest quality tier available (4K, 1080p, 720p, etc.).",
    )
    "Highest quality tier available (4K, 1080p, 720p, etc.)."
    primary_width: Optional[int] = Field(
        alias="primaryWidth", description="Primary video track width in pixels."
    )
    "Primary video track width in pixels."
    primary_height: Optional[int] = Field(
        alias="primaryHeight", description="Primary video track height in pixels."
    )
    "Primary video track height in pixels."
    primary_fps: Optional[float] = Field(
        alias="primaryFps", description="Primary video track framerate."
    )
    "Primary video track framerate."
    primary_codec: Optional[str] = Field(
        alias="primaryCodec",
        description="Primary video codec (H.264, H.265, VP9, AV1).",
    )
    "Primary video codec (H.264, H.265, VP9, AV1)."
    primary_bitrate: Optional[int] = Field(
        alias="primaryBitrate", description="Primary video bitrate in kbps."
    )
    "Primary video bitrate in kbps."
    has_issues: Optional[bool] = Field(
        alias="hasIssues", description="Whether the stream has active quality issues."
    )
    "Whether the stream has active quality issues."
    issues_description: Optional[str] = Field(
        alias="issuesDescription",
        description="Human-readable description of current issues.",
    )
    "Human-readable description of current issues."


class ViewerSessionDefaultStreamPushTargets(BaseModel):
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


class ViewerSessionDefaultStreamPlaybackPolicy(BaseModel):
    """Per-playback-object access policy. Foghorn reads this in the USER_NEW
    trigger handler; the requiresAuth marker (on the playback object itself)
    gates whether the full policy is fetched at all."""

    type_: PlaybackPolicyType = Field(alias="type")
    allowed_origins: list[str] = Field(
        alias="allowedOrigins",
        description="Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied.",
    )
    "Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied."


class ViewerSessionDefaultStreamThumbnailAssets(BaseModel):
    """Chandler-served thumbnail asset URLs (poster, sprite, VTT cues). URL shape
    is `{chandlerBase}/assets/{assetKey}/poster.jpg`,
    `/assets/{assetKey}/sprite.jpg`, `/assets/{assetKey}/sprite.vtt` — Chandler
    serves the object key directly, no version resolution. assetKey is stream_id
    for live streams; clip_hash / dvr_hash / vod_hash (= artifact_hash) for artifacts."""

    poster_url: str = Field(alias="posterUrl")
    sprite_vtt_url: str = Field(alias="spriteVttUrl")
    sprite_jpg_url: str = Field(alias="spriteJpgUrl")
    asset_key: str = Field(alias="assetKey")


class ViewerSessionDefaultStreamRetentionOverrides(BaseModel):
    """Per-stream retention overrides for DVR and clips. VOD uploads aren't
    stream-bound, so they have no stream-level knob here."""

    stream_id: str = Field(alias="streamId")
    dvr_retention_days_override: Optional[int] = Field(
        alias="dvrRetentionDaysOverride",
        description="Null = no override (inherit tenant default). 0 = no auto-expire.",
    )
    "Null = no override (inherit tenant default). 0 = no auto-expire."
    clip_retention_days_override: Optional[int] = Field(
        alias="clipRetentionDaysOverride"
    )


class ViewerSessionDefaultClientBucket(BaseModel):
    h_3_index: str = Field(alias="h3Index")
    resolution: int


class ViewerSessionInNodeDefault(BaseModel):
    id: str
    timestamp: datetime
    stream_id: str = Field(alias="streamId")
    stream: Optional["ViewerSessionInNodeDefaultStream"]
    viewer_session_node_id: Optional[str] = Field(alias="viewerSessionNodeId")
    session_id: str = Field(alias="sessionId")
    connected_at: Optional[datetime] = Field(alias="connectedAt")
    disconnected_at: Optional[datetime] = Field(alias="disconnectedAt")
    viewer_session_connector: Optional[str] = Field(alias="viewerSessionConnector")
    country_code: Optional[str] = Field(alias="countryCode")
    city: Optional[str]
    latitude: Optional[float]
    longitude: Optional[float]
    duration_seconds: Optional[int] = Field(alias="durationSeconds")
    bytes_up: Optional[float] = Field(alias="bytesUp")
    bytes_down: Optional[float] = Field(alias="bytesDown")
    connection_quality: Optional[float] = Field(alias="connectionQuality")
    buffer_health: Optional[float] = Field(alias="bufferHealth")
    client_bucket: Optional["ViewerSessionInNodeDefaultClientBucket"] = Field(
        alias="clientBucket"
    )


class ViewerSessionInNodeDefaultStream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

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
        description="Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path.",
    )
    "Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path."
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
    pull_source: Optional["ViewerSessionInNodeDefaultStreamPullSource"] = Field(
        alias="pullSource",
        description="Pull-source config for pull streams; null for push streams.",
    )
    "Pull-source config for pull streams; null for push streams."
    managed_source: Optional["ViewerSessionInNodeDefaultStreamManagedSource"] = Field(
        alias="managedSource",
        description="Safe source summary for managed streams; null for push and pull streams.",
    )
    "Safe source summary for managed streams; null for push and pull streams."
    source_location: "ViewerSessionInNodeDefaultStreamSourceLocation" = Field(
        alias="sourceLocation",
        description="Where the stream's source may be ingested, derived from the stream's own ingest placement rules.",
    )
    "Where the stream's source may be ingested, derived from the stream's own ingest placement rules."
    created_at: datetime = Field(
        alias="createdAt", description="When this stream was created."
    )
    "When this stream was created."
    updated_at: datetime = Field(
        alias="updatedAt", description="When this stream was last modified."
    )
    "When this stream was last modified."
    metrics: Optional["ViewerSessionInNodeDefaultStreamMetrics"] = Field(
        description="Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    )
    "Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    push_targets: list["ViewerSessionInNodeDefaultStreamPushTargets"] = Field(
        alias="pushTargets",
        description="Configured multistream push targets for this stream.",
    )
    "Configured multistream push targets for this stream."
    playback_policy: Optional["ViewerSessionInNodeDefaultStreamPlaybackPolicy"] = Field(
        alias="playbackPolicy",
        description="Playback access policy. null/PUBLIC = anyone with the playbackId can watch.",
    )
    "Playback access policy. null/PUBLIC = anyone with the playbackId can watch."
    dvr_chapter_mode: Optional[DVRChapterMode] = Field(
        alias="dvrChapterMode",
        description="How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast.",
    )
    "How saved recordings are split into chapters. Snapshotted when a recording\nstarts; changes apply from the next broadcast. NONE = live rewind only,\nnothing kept after the broadcast."
    dvr_chapter_interval_seconds: Optional[int] = Field(
        alias="dvrChapterIntervalSeconds",
        description="Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour).",
    )
    "Chapter interval in seconds. Required when dvrChapterMode = FIXED_INTERVAL,\nignored otherwise. Minimum 3600 (1 hour)."
    monitoring: MonitoringToggle = Field(
        description="Per-stream Skipper monitoring override (INHERIT follows tier)."
    )
    "Per-stream Skipper monitoring override (INHERIT follows tier)."
    thumbnail_assets: Optional["ViewerSessionInNodeDefaultStreamThumbnailAssets"] = (
        Field(
            alias="thumbnailAssets",
            description="Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles.",
        )
    )
    "Server-resolved Chandler URLs for the stream's poster and sprite\nthumbnails. Derived from active_ingest_cluster_id + stream_id at SELECT\ntime. Null when the stream has never been live; the poster.jpg 404s\nuntil Helmsman uploads its first frame, which the player's fallback\nchain handles."
    retention_overrides: Optional[
        "ViewerSessionInNodeDefaultStreamRetentionOverrides"
    ] = Field(
        alias="retentionOverrides",
        description="Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here.",
    )
    "Per-stream retention overrides for DVR and clips. Null when the stream\nhas no overrides set (inherits the tenant default). VOD uploads aren't\nstream-bound, so they don't appear here."


class ViewerSessionInNodeDefaultStreamPullSource(BaseModel):
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


class ViewerSessionInNodeDefaultStreamManagedSource(BaseModel):
    """Safe summary of an operator-managed source. Literal paths, commands, and
    credentials are intentionally not exposed through the tenant API."""

    source_kind: str = Field(
        alias="sourceKind", description="Managed source form: file, playlist, or exec."
    )
    "Managed source form: file, playlist, or exec."
    always_on: bool = Field(
        alias="alwaysOn",
        description="Whether the source is kept active without waiting for a viewer.",
    )
    "Whether the source is kept active without waiting for a viewer."
    placement_count: int = Field(
        alias="placementCount",
        description="Number of source placements requested by the operator configuration.",
    )
    "Number of source placements requested by the operator configuration."


class ViewerSessionInNodeDefaultStreamSourceLocation(BaseModel):
    mode: SourceLocationMode
    avoid_node_ids: list[str] = Field(
        alias="avoidNodeIds", description="Empty unless mode is RESTRICTED."
    )
    "Empty unless mode is RESTRICTED."


class ViewerSessionInNodeDefaultStreamMetrics(BaseModel):
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
    node_id: Optional[str] = Field(
        alias="nodeId", description="ID of the edge node handling this stream."
    )
    "ID of the edge node handling this stream."
    track_count: Optional[int] = Field(
        alias="trackCount", description="Number of quality tracks available."
    )
    "Number of quality tracks available."
    total_inputs: Optional[int] = Field(
        alias="totalInputs", description="Total ingest connections to this stream."
    )
    "Total ingest connections to this stream."
    uploaded_bytes: float = Field(
        alias="uploadedBytes",
        description="Total bytes uploaded (ingested) for current session.",
    )
    "Total bytes uploaded (ingested) for current session."
    downloaded_bytes: float = Field(
        alias="downloadedBytes",
        description="Total bytes downloaded (egress) for current session.",
    )
    "Total bytes downloaded (egress) for current session."
    viewer_seconds: float = Field(
        alias="viewerSeconds", description="Total viewer-seconds accumulated."
    )
    "Total viewer-seconds accumulated."
    packets_sent: Optional[float] = Field(
        alias="packetsSent", description="Total packets sent to viewers."
    )
    "Total packets sent to viewers."
    packets_lost: Optional[float] = Field(
        alias="packetsLost", description="Total packets lost in transit."
    )
    "Total packets lost in transit."
    packets_retransmitted: Optional[float] = Field(
        alias="packetsRetransmitted", description="Total packets retransmitted."
    )
    "Total packets retransmitted."
    buffer_state: Optional[str] = Field(
        alias="bufferState",
        description="Buffer health state (HEALTHY, WARNING, CRITICAL).",
    )
    "Buffer health state (HEALTHY, WARNING, CRITICAL)."
    quality_tier: Optional[str] = Field(
        alias="qualityTier",
        description="Highest quality tier available (4K, 1080p, 720p, etc.).",
    )
    "Highest quality tier available (4K, 1080p, 720p, etc.)."
    primary_width: Optional[int] = Field(
        alias="primaryWidth", description="Primary video track width in pixels."
    )
    "Primary video track width in pixels."
    primary_height: Optional[int] = Field(
        alias="primaryHeight", description="Primary video track height in pixels."
    )
    "Primary video track height in pixels."
    primary_fps: Optional[float] = Field(
        alias="primaryFps", description="Primary video track framerate."
    )
    "Primary video track framerate."
    primary_codec: Optional[str] = Field(
        alias="primaryCodec",
        description="Primary video codec (H.264, H.265, VP9, AV1).",
    )
    "Primary video codec (H.264, H.265, VP9, AV1)."
    primary_bitrate: Optional[int] = Field(
        alias="primaryBitrate", description="Primary video bitrate in kbps."
    )
    "Primary video bitrate in kbps."
    has_issues: Optional[bool] = Field(
        alias="hasIssues", description="Whether the stream has active quality issues."
    )
    "Whether the stream has active quality issues."
    issues_description: Optional[str] = Field(
        alias="issuesDescription",
        description="Human-readable description of current issues.",
    )
    "Human-readable description of current issues."


class ViewerSessionInNodeDefaultStreamPushTargets(BaseModel):
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


class ViewerSessionInNodeDefaultStreamPlaybackPolicy(BaseModel):
    """Per-playback-object access policy. Foghorn reads this in the USER_NEW
    trigger handler; the requiresAuth marker (on the playback object itself)
    gates whether the full policy is fetched at all."""

    type_: PlaybackPolicyType = Field(alias="type")
    allowed_origins: list[str] = Field(
        alias="allowedOrigins",
        description="Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied.",
    )
    "Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied."


class ViewerSessionInNodeDefaultStreamThumbnailAssets(BaseModel):
    """Chandler-served thumbnail asset URLs (poster, sprite, VTT cues). URL shape
    is `{chandlerBase}/assets/{assetKey}/poster.jpg`,
    `/assets/{assetKey}/sprite.jpg`, `/assets/{assetKey}/sprite.vtt` — Chandler
    serves the object key directly, no version resolution. assetKey is stream_id
    for live streams; clip_hash / dvr_hash / vod_hash (= artifact_hash) for artifacts."""

    poster_url: str = Field(alias="posterUrl")
    sprite_vtt_url: str = Field(alias="spriteVttUrl")
    sprite_jpg_url: str = Field(alias="spriteJpgUrl")
    asset_key: str = Field(alias="assetKey")


class ViewerSessionInNodeDefaultStreamRetentionOverrides(BaseModel):
    """Per-stream retention overrides for DVR and clips. VOD uploads aren't
    stream-bound, so they have no stream-level knob here."""

    stream_id: str = Field(alias="streamId")
    dvr_retention_days_override: Optional[int] = Field(
        alias="dvrRetentionDaysOverride",
        description="Null = no override (inherit tenant default). 0 = no auto-expire.",
    )
    "Null = no override (inherit tenant default). 0 = no auto-expire."
    clip_retention_days_override: Optional[int] = Field(
        alias="clipRetentionDaysOverride"
    )


class ViewerSessionInNodeDefaultClientBucket(BaseModel):
    h_3_index: str = Field(alias="h3Index")
    resolution: int


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


class VodAssetInNodeDefault(BaseModel):
    """A Video-on-Demand asset uploaded by the tenant.
    VOD assets can be played back using the playbackId in playback URLs."""

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
    vod_asset_stream_id: Optional[str] = Field(
        alias="vodAssetStreamId",
        description="Source stream UUID for stream-derived VOD artifacts such as DVR chapters.",
    )
    "Source stream UUID for stream-derived VOD artifacts such as DVR chapters."
    origin_type: Optional[str] = Field(
        alias="originType",
        description="Registry origin kind. Null/user_upload for ordinary uploads; dvr_chapter for finalized DVR chapters.",
    )
    "Registry origin kind. Null/user_upload for ordinary uploads; dvr_chapter for finalized DVR chapters."
    origin_id: Optional[str] = Field(
        alias="originId",
        description="Origin entity id, such as DVR chapter_id when originType=dvr_chapter.",
    )
    "Origin entity id, such as DVR chapter_id when originType=dvr_chapter."
    vod_asset_title: Optional[str] = Field(
        alias="vodAssetTitle", description="Optional display title for the asset."
    )
    "Optional display title for the asset."
    description: Optional[str] = Field(
        description="Optional description of the asset content."
    )
    "Optional description of the asset content."
    filename: Optional[str] = Field(description="Original filename when uploaded.")
    "Original filename when uploaded."
    vod_asset_status: VodAssetStatus = Field(
        alias="vodAssetStatus",
        description="Current processing/storage status of the asset.",
    )
    "Current processing/storage status of the asset."
    vod_asset_storage_location: str = Field(
        alias="vodAssetStorageLocation",
        description="Where the asset is stored (s3, local, freezing).",
    )
    "Where the asset is stored (s3, local, freezing)."
    sync_status: Optional[str] = Field(
        alias="syncStatus",
        description="Current S3 sync state (pending, in_progress, synced, failed, lost_local).",
    )
    "Current S3 sync state (pending, in_progress, synced, failed, lost_local)."
    has_local_copy: Optional[bool] = Field(
        alias="hasLocalCopy",
        description="Present full local node copy (origin or cache): true when at least one node holds a complete local copy, false when none remain (playback via Helmsman's read-through relay from S3). Null when the placement overlay (Periscope) is unavailable — unknown, not 'no local copy'. Durable S3-only state is derived by consumers as isSynced && hasLocalCopy == false.",
    )
    "Present full local node copy (origin or cache): true when at least one node holds a complete local copy, false when none remain (playback via Helmsman's read-through relay from S3). Null when the placement overlay (Periscope) is unavailable — unknown, not 'no local copy'. Durable S3-only state is derived by consumers as isSynced && hasLocalCopy == false."
    vod_asset_is_synced: bool = Field(
        alias="vodAssetIsSynced", description="True when S3 has an authoritative copy."
    )
    "True when S3 has an authoritative copy."
    vod_asset_is_finalized: bool = Field(
        alias="vodAssetIsFinalized",
        description="True when the S3 copy includes the Mist .dtsh index.",
    )
    "True when the S3 copy includes the Mist .dtsh index."
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
    vod_asset_updated_at: datetime = Field(
        alias="vodAssetUpdatedAt", description="When the asset was last modified."
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
    playback_policy: Optional["VodAssetInNodeDefaultPlaybackPolicy"] = Field(
        alias="playbackPolicy",
        description="Playback access policy. null/PUBLIC = anyone with the playbackId can watch.",
    )
    "Playback access policy. null/PUBLIC = anyone with the playbackId can watch."
    thumbnail_assets: Optional["VodAssetInNodeDefaultThumbnailAssets"] = Field(
        alias="thumbnailAssets",
        description="Server-resolved Chandler URLs for the VOD's poster and sprite thumbnails.\nNull until Foghorn confirms the thumbnail upload.",
    )
    "Server-resolved Chandler URLs for the VOD's poster and sprite thumbnails.\nNull until Foghorn confirms the thumbnail upload."
    effective_retention: Optional["VodAssetInNodeDefaultEffectiveRetention"] = Field(
        alias="effectiveRetention",
        description="Resolved retention horizon with the source of the decision (per-asset\noverride → per-stream override → tenant default → tier entitlement).\nNull while the asset's retention_until column is unset (infinite).",
    )
    "Resolved retention horizon with the source of the decision (per-asset\noverride → per-stream override → tenant default → tier entitlement).\nNull while the asset's retention_until column is unset (infinite)."
    storage_cost: Optional["VodAssetInNodeDefaultStorageCost"] = Field(
        alias="storageCost",
        description="Marginal storage cost for this asset on the tenant's tier. Null when\nthe tenant has no storage meter (self-hosted, fully tenant-private\ncluster). Computed from sizeBytes in GiB × the tier's price per GiB-month.",
    )
    "Marginal storage cost for this asset on the tenant's tier. Null when\nthe tenant has no storage meter (self-hosted, fully tenant-private\ncluster). Computed from sizeBytes in GiB × the tier's price per GiB-month."


class VodAssetInNodeDefaultPlaybackPolicy(BaseModel):
    """Per-playback-object access policy. Foghorn reads this in the USER_NEW
    trigger handler; the requiresAuth marker (on the playback object itself)
    gates whether the full policy is fetched at all."""

    type_: PlaybackPolicyType = Field(alias="type")
    jwt: Optional["VodAssetInNodeDefaultPlaybackPolicyJwt"] = Field(
        description="JWT-policy details, populated when type == JWT."
    )
    "JWT-policy details, populated when type == JWT."
    webhook: Optional["VodAssetInNodeDefaultPlaybackPolicyWebhook"] = Field(
        description="Webhook-policy details, populated when type == WEBHOOK. Secret is masked."
    )
    "Webhook-policy details, populated when type == WEBHOOK. Secret is masked."
    allowed_origins: list[str] = Field(
        alias="allowedOrigins",
        description="Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied.",
    )
    "Sites allowed to embed the content, as normalized `scheme://host[:port]`\norigins; `*` allows any. Empty = no restriction. A browser viewer whose\nOrigin (or Referer) is not listed is denied."


class VodAssetInNodeDefaultPlaybackPolicyJwt(BaseModel):
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


class VodAssetInNodeDefaultPlaybackPolicyWebhook(BaseModel):
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
    context: Optional[Any] = Field(
        description="Your JSON object, sent as `context` in every access request to the URL. Null when unset."
    )
    "Your JSON object, sent as `context` in every access request to the URL. Null when unset."


class VodAssetInNodeDefaultThumbnailAssets(BaseModel):
    """Chandler-served thumbnail asset URLs (poster, sprite, VTT cues). URL shape
    is `{chandlerBase}/assets/{assetKey}/poster.jpg`,
    `/assets/{assetKey}/sprite.jpg`, `/assets/{assetKey}/sprite.vtt` — Chandler
    serves the object key directly, no version resolution. assetKey is stream_id
    for live streams; clip_hash / dvr_hash / vod_hash (= artifact_hash) for artifacts."""

    poster_url: str = Field(alias="posterUrl")
    sprite_vtt_url: str = Field(alias="spriteVttUrl")
    sprite_jpg_url: str = Field(alias="spriteJpgUrl")
    asset_key: str = Field(alias="assetKey")


class VodAssetInNodeDefaultEffectiveRetention(BaseModel):
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


class VodAssetInNodeDefaultStorageCost(BaseModel):
    """Marginal per-asset storage cost projection. Used by the customer-facing
    storage browser to show "this clip costs you ~$0.01/day". The unit is
    the tier's currency (typically EUR). Rating prices storage per GiB-month
    (a fixed 730-hour month); perMonth = GiB × price, perDay = perMonth × 24 / 730."""

    per_day: float = Field(alias="perDay")
    per_month: float = Field(alias="perMonth")
    currency: str


class VodRetentionAssetDefault(BaseModel):
    """A VOD asset with retention data in the window. Eligibility + stats (sessions,
    duration, lastSeen) come from analytics; title/playbackId are composed from the
    catalog by artifactHash — both may be null when the asset is uncatalogued (e.g.
    deleted but retention still within TTL)."""

    artifact_hash: str = Field(alias="artifactHash")
    total_sessions: int = Field(alias="totalSessions")
    duration_s: int = Field(alias="durationS")
    last_seen: datetime = Field(alias="lastSeen")
    title: Optional[str]
    playback_id: Optional[str] = Field(alias="playbackId")


class VodRetentionDefault(BaseModel):
    """VOD retention curve for an artifact. retention(T) = points[T].reached /
    totalSessions (monotonic non-increasing); density(T) = points[T].secondsWatched
    (the "most replayed" curve). Buckets are fixed-width (bucketWidthS) along the asset
    timeline. These differ because a seek-to-end raises reach without adding density."""

    bucket_width_s: int = Field(alias="bucketWidthS")
    asset_duration_s: int = Field(alias="assetDurationS")
    total_sessions: int = Field(alias="totalSessions")
    points: list["VodRetentionDefaultPoints"]


class VodRetentionDefaultPoints(BaseModel):
    """One point of the VOD retention curve: a fixed-width timeline bucket with its
    watched-seconds density and the count of sessions that reached it."""

    bucket_index: int = Field(alias="bucketIndex")
    seconds_watched: float = Field(alias="secondsWatched")
    reached: int = Field(
        description="Sessions whose furthest playhead position is at or beyond this bucket (audience retention numerator)."
    )
    "Sessions whose furthest playhead position is at or beyond this bucket (audience retention numerator)."


class WalletIdentityDefault(BaseModel):
    """A linked cryptocurrency wallet for authentication.
    Users can link multiple wallets across different chains."""

    id: str = Field(description="Unique identifier for this wallet link.")
    "Unique identifier for this wallet link."
    address: str = Field(
        description="The wallet address (chain-specific format, e.g. 0x... for Ethereum)."
    )
    "The wallet address (chain-specific format, e.g. 0x... for Ethereum)."
    created_at: datetime = Field(
        alias="createdAt", description="When this wallet was linked."
    )
    "When this wallet was linked."
    last_auth_at: Optional[datetime] = Field(
        alias="lastAuthAt",
        description="When this wallet was last used for authentication.",
    )
    "When this wallet was last used for authentication."


class WalletLoginPayloadDefault(BaseModel):
    """Successful wallet login response containing JWT and user info."""

    token: str = Field(description="JWT access token for API authentication.")
    "JWT access token for API authentication."
    user: "WalletLoginPayloadDefaultUser" = Field(description="The authenticated user.")
    "The authenticated user."
    expires_at: datetime = Field(
        alias="expiresAt", description="When the token expires."
    )
    "When the token expires."
    is_new_account: bool = Field(
        alias="isNewAccount",
        description="True if this was a new account created by the login.",
    )
    "True if this was a new account created by the login."


class WalletLoginPayloadDefaultUser(BaseModel):
    id: str
    email: Optional[str] = Field(
        description="User's email address. Null for wallet-only accounts."
    )
    "User's email address. Null for wallet-only accounts."
    name: Optional[str]
    role: str
    created_at: datetime = Field(alias="createdAt")
    wallets: list["WalletLoginPayloadDefaultUserWallets"] = Field(
        description="Linked wallet addresses for this user."
    )
    "Linked wallet addresses for this user."


class WalletLoginPayloadDefaultUserWallets(BaseModel):
    """A linked cryptocurrency wallet for authentication.
    Users can link multiple wallets across different chains."""

    id: str = Field(description="Unique identifier for this wallet link.")
    "Unique identifier for this wallet link."
    address: str = Field(
        description="The wallet address (chain-specific format, e.g. 0x... for Ethereum)."
    )
    "The wallet address (chain-specific format, e.g. 0x... for Ethereum)."
    created_at: datetime = Field(
        alias="createdAt", description="When this wallet was linked."
    )
    "When this wallet was linked."
    last_auth_at: Optional[datetime] = Field(
        alias="lastAuthAt",
        description="When this wallet was last used for authentication.",
    )
    "When this wallet was last used for authentication."


class WebhookDeliveryDefault(BaseModel):
    """One delivery of one event to one endpoint, or a test delivery."""

    id: str = Field(description="Delivery ID. A replay keeps it.")
    "Delivery ID. A replay keeps it."
    endpoint_id: str = Field(alias="endpointId")
    event_id: Optional[str] = Field(
        alias="eventId",
        description="The event ID, sent as webhook-id and as the body's id. Null for test deliveries.",
    )
    "The event ID, sent as webhook-id and as the body's id. Null for test deliveries."
    event_type: str = Field(alias="eventType")
    kind: WebhookDeliveryKind
    status: WebhookDeliveryStatus
    attempts: int = Field(
        description="Attempts since the delivery was created or last replayed."
    )
    "Attempts since the delivery was created or last replayed."
    next_attempt_at: Optional[datetime] = Field(
        alias="nextAttemptAt",
        description="When the next attempt is due; null unless pending.",
    )
    "When the next attempt is due; null unless pending."
    last_status_code: int = Field(
        alias="lastStatusCode",
        description="HTTP status of the last attempt; 0 when no response arrived.",
    )
    "HTTP status of the last attempt; 0 when no response arrived."
    last_error_class: str = Field(
        alias="lastErrorClass",
        description="Failure class of the last attempt: http_status, redirect, timeout, connection, tls, dns, or blocked_destination; internal when FrameWorks could not sign or render the delivery; empty after a success.",
    )
    "Failure class of the last attempt: http_status, redirect, timeout, connection, tls, dns, or blocked_destination; internal when FrameWorks could not sign or render the delivery; empty after a success."
    delivered_at: Optional[datetime] = Field(alias="deliveredAt")
    replay_count: int = Field(alias="replayCount")
    last_replayed_at: Optional[datetime] = Field(alias="lastReplayedAt")
    created_at: datetime = Field(alias="createdAt")
    updated_at: datetime = Field(alias="updatedAt")
    attempt_history: list["WebhookDeliveryDefaultAttemptHistory"] = Field(
        alias="attemptHistory",
        description="Every HTTP attempt, oldest first, including attempts before the last replay.",
    )
    "Every HTTP attempt, oldest first, including attempts before the last replay."


class WebhookDeliveryDefaultAttemptHistory(BaseModel):
    """One HTTP attempt of a delivery."""

    id: str
    attempt_number: int = Field(alias="attemptNumber")
    status_code: int = Field(
        alias="statusCode", description="HTTP status; 0 when no response arrived."
    )
    "HTTP status; 0 when no response arrived."
    error_class: str = Field(alias="errorClass")
    latency_ms: int = Field(alias="latencyMs")
    response_excerpt: str = Field(
        alias="responseExcerpt", description="At most 1 KiB of the response body."
    )
    "At most 1 KiB of the response body."
    attempted_at: datetime = Field(alias="attemptedAt")


class WebhookEndpointDefault(BaseModel):
    """An outbound webhook endpoint. FrameWorks POSTs the tenant's public events of
    the subscribed types to its URL, signed with the Standard Webhooks scheme
    (webhook-id, webhook-timestamp, webhook-signature headers)."""

    id: str = Field(description="Endpoint ID.")
    "Endpoint ID."
    url: str = Field(description="The https URL deliveries are sent to.")
    "The https URL deliveries are sent to."
    description: str = Field(description="Free-text description.")
    "Free-text description."
    event_types: list[str] = Field(
        alias="eventTypes",
        description='Public event types the endpoint receives; "*" receives every type.',
    )
    'Public event types the endpoint receives; "*" receives every type.'
    api_version: str = Field(
        alias="apiVersion",
        description='Public event package version the payloads are rendered with, e.g. "v1".',
    )
    'Public event package version the payloads are rendered with, e.g. "v1".'
    status: WebhookEndpointStatus
    disabled_reason: Optional[WebhookEndpointDisabledReason] = Field(
        alias="disabledReason",
        description="Why the endpoint is disabled; null while enabled.",
    )
    "Why the endpoint is disabled; null while enabled."
    disabled_at: Optional[datetime] = Field(
        alias="disabledAt",
        description="When the endpoint was disabled; null while enabled.",
    )
    "When the endpoint was disabled; null while enabled."
    consecutive_failures: int = Field(
        alias="consecutiveFailures",
        description="Failed attempts since the last successful delivery.",
    )
    "Failed attempts since the last successful delivery."
    failing_since: Optional[datetime] = Field(
        alias="failingSince",
        description="First failed attempt since the last successful delivery.",
    )
    "First failed attempt since the last successful delivery."
    last_success_at: Optional[datetime] = Field(alias="lastSuccessAt")
    last_failure_at: Optional[datetime] = Field(alias="lastFailureAt")
    previous_secret_expires_at: Optional[datetime] = Field(
        alias="previousSecretExpiresAt",
        description="Until when the previous signing secret still signs deliveries; null when there is none.",
    )
    "Until when the previous signing secret still signs deliveries; null when there is none."
    created_at: datetime = Field(alias="createdAt")
    updated_at: datetime = Field(alias="updatedAt")


class WebhookEndpointSecretDefault(BaseModel):
    """An endpoint with its signing secret, returned only when the secret is created
    or rotated."""

    endpoint: "WebhookEndpointSecretDefaultEndpoint"
    secret: str = Field(
        description='The signing secret, "whsec_" followed by base64. Store it now; it is never shown again.'
    )
    'The signing secret, "whsec_" followed by base64. Store it now; it is never shown again.'


class WebhookEndpointSecretDefaultEndpoint(BaseModel):
    """An outbound webhook endpoint. FrameWorks POSTs the tenant's public events of
    the subscribed types to its URL, signed with the Standard Webhooks scheme
    (webhook-id, webhook-timestamp, webhook-signature headers)."""

    id: str = Field(description="Endpoint ID.")
    "Endpoint ID."
    url: str = Field(description="The https URL deliveries are sent to.")
    "The https URL deliveries are sent to."
    description: str = Field(description="Free-text description.")
    "Free-text description."
    event_types: list[str] = Field(
        alias="eventTypes",
        description='Public event types the endpoint receives; "*" receives every type.',
    )
    'Public event types the endpoint receives; "*" receives every type.'
    api_version: str = Field(
        alias="apiVersion",
        description='Public event package version the payloads are rendered with, e.g. "v1".',
    )
    'Public event package version the payloads are rendered with, e.g. "v1".'
    status: WebhookEndpointStatus
    disabled_reason: Optional[WebhookEndpointDisabledReason] = Field(
        alias="disabledReason",
        description="Why the endpoint is disabled; null while enabled.",
    )
    "Why the endpoint is disabled; null while enabled."
    disabled_at: Optional[datetime] = Field(
        alias="disabledAt",
        description="When the endpoint was disabled; null while enabled.",
    )
    "When the endpoint was disabled; null while enabled."
    consecutive_failures: int = Field(
        alias="consecutiveFailures",
        description="Failed attempts since the last successful delivery.",
    )
    "Failed attempts since the last successful delivery."
    failing_since: Optional[datetime] = Field(
        alias="failingSince",
        description="First failed attempt since the last successful delivery.",
    )
    "First failed attempt since the last successful delivery."
    last_success_at: Optional[datetime] = Field(alias="lastSuccessAt")
    last_failure_at: Optional[datetime] = Field(alias="lastFailureAt")
    previous_secret_expires_at: Optional[datetime] = Field(
        alias="previousSecretExpiresAt",
        description="Until when the previous signing secret still signs deliveries; null when there is none.",
    )
    "Until when the previous signing secret still signs deliveries; null when there is none."
    created_at: datetime = Field(alias="createdAt")
    updated_at: datetime = Field(alias="updatedAt")


class WebhookReplayResultDefault(BaseModel):
    """The result of a range replay."""

    replayed_count: int = Field(alias="replayedCount")
    has_more: bool = Field(
        alias="hasMore",
        description="More failed or skipped deliveries remain in the range.",
    )
    "More failed or skipped deliveries remain in the range."


class WebhookTestResultDefault(BaseModel):
    """The result of a test delivery."""

    delivery: "WebhookTestResultDefaultDelivery"
    attempt: "WebhookTestResultDefaultAttempt"


class WebhookTestResultDefaultDelivery(BaseModel):
    """One delivery of one event to one endpoint, or a test delivery."""

    id: str = Field(description="Delivery ID. A replay keeps it.")
    "Delivery ID. A replay keeps it."
    endpoint_id: str = Field(alias="endpointId")
    event_id: Optional[str] = Field(
        alias="eventId",
        description="The event ID, sent as webhook-id and as the body's id. Null for test deliveries.",
    )
    "The event ID, sent as webhook-id and as the body's id. Null for test deliveries."
    event_type: str = Field(alias="eventType")
    kind: WebhookDeliveryKind
    status: WebhookDeliveryStatus
    attempts: int = Field(
        description="Attempts since the delivery was created or last replayed."
    )
    "Attempts since the delivery was created or last replayed."
    next_attempt_at: Optional[datetime] = Field(
        alias="nextAttemptAt",
        description="When the next attempt is due; null unless pending.",
    )
    "When the next attempt is due; null unless pending."
    last_status_code: int = Field(
        alias="lastStatusCode",
        description="HTTP status of the last attempt; 0 when no response arrived.",
    )
    "HTTP status of the last attempt; 0 when no response arrived."
    last_error_class: str = Field(
        alias="lastErrorClass",
        description="Failure class of the last attempt: http_status, redirect, timeout, connection, tls, dns, or blocked_destination; internal when FrameWorks could not sign or render the delivery; empty after a success.",
    )
    "Failure class of the last attempt: http_status, redirect, timeout, connection, tls, dns, or blocked_destination; internal when FrameWorks could not sign or render the delivery; empty after a success."
    delivered_at: Optional[datetime] = Field(alias="deliveredAt")
    replay_count: int = Field(alias="replayCount")
    last_replayed_at: Optional[datetime] = Field(alias="lastReplayedAt")
    created_at: datetime = Field(alias="createdAt")
    updated_at: datetime = Field(alias="updatedAt")
    attempt_history: list["WebhookTestResultDefaultDeliveryAttemptHistory"] = Field(
        alias="attemptHistory",
        description="Every HTTP attempt, oldest first, including attempts before the last replay.",
    )
    "Every HTTP attempt, oldest first, including attempts before the last replay."


class WebhookTestResultDefaultDeliveryAttemptHistory(BaseModel):
    """One HTTP attempt of a delivery."""

    id: str
    attempt_number: int = Field(alias="attemptNumber")
    status_code: int = Field(
        alias="statusCode", description="HTTP status; 0 when no response arrived."
    )
    "HTTP status; 0 when no response arrived."
    error_class: str = Field(alias="errorClass")
    latency_ms: int = Field(alias="latencyMs")
    response_excerpt: str = Field(
        alias="responseExcerpt", description="At most 1 KiB of the response body."
    )
    "At most 1 KiB of the response body."
    attempted_at: datetime = Field(alias="attemptedAt")


class WebhookTestResultDefaultAttempt(BaseModel):
    """One HTTP attempt of a delivery."""

    id: str
    attempt_number: int = Field(alias="attemptNumber")
    status_code: int = Field(
        alias="statusCode", description="HTTP status; 0 when no response arrived."
    )
    "HTTP status; 0 when no response arrived."
    error_class: str = Field(alias="errorClass")
    latency_ms: int = Field(alias="latencyMs")
    response_excerpt: str = Field(
        alias="responseExcerpt", description="At most 1 KiB of the response body."
    )
    "At most 1 KiB of the response body."
    attempted_at: datetime = Field(alias="attemptedAt")


class X402PaymentResultDefault(BaseModel):
    """Result from submitting an x402 payment for settlement."""

    success: bool = Field(description="Whether the payment settlement succeeded.")
    "Whether the payment settlement succeeded."
    is_auth_only: bool = Field(
        alias="isAuthOnly",
        description="True if this was an auth-only payload (always false for settlement).",
    )
    "True if this was an auth-only payload (always false for settlement)."
    tenant_id: str = Field(alias="tenantId", description="Tenant that was credited.")
    "Tenant that was credited."
    wallet_address: str = Field(
        alias="walletAddress", description="Wallet address that paid."
    )
    "Wallet address that paid."
    credited_cents: int = Field(
        alias="creditedCents", description="Amount credited in cents."
    )
    "Amount credited in cents."
    new_balance_cents: Optional[int] = Field(
        alias="newBalanceCents", description="New balance in cents (if available)."
    )
    "New balance in cents (if available)."
    tx_hash: Optional[str] = Field(
        alias="txHash", description="Blockchain transaction hash (if available)."
    )
    "Blockchain transaction hash (if available)."
    message: str = Field(description="Human-readable status message.")
    "Human-readable status message."


APIUsageRecordDefault.model_rebuild()
ArtifactEventDefault.model_rebuild()
ArtifactEventInNodeDefault.model_rebuild()
ArtifactStateDefault.model_rebuild()
AssetNodeCopiesDefault.model_rebuild()
AuthErrorDefault.model_rebuild()
AuthError.model_rebuild()
AvailableClusterDefault.model_rebuild()
BalanceTransactionDefault.model_rebuild()
BillingDetailsDefault.model_rebuild()
BillingStatusDefault.model_rebuild()
BillingTierDefault.model_rebuild()
BootstrapEdgeResponseDefault.model_rebuild()
BufferEventDefault.model_rebuild()
BufferEventInNodeDefault.model_rebuild()
CapabilitiesDefault.model_rebuild()
CardTopupResultDefault.model_rebuild()
ChangeBillingTierPayloadDefault.model_rebuild()
ClientMetrics5mDefault.model_rebuild()
ClientQoeSummaryDefault.model_rebuild()
EffectiveRetention.model_rebuild()
PlaybackPolicy.model_rebuild()
ThumbnailAssets.model_rebuild()
Clip.model_rebuild()
ClipInNodeDefault.model_rebuild()
ClusterAccessDefault.model_rebuild()
ClusterBootOpsDefault.model_rebuild()
ClusterDefault.model_rebuild()
ClusterInNodeDefault.model_rebuild()
ClusterInviteDefault.model_rebuild()
ClusterPairTrafficDefault.model_rebuild()
ClusterQoeOpsDefault.model_rebuild()
ClusterSubscriptionDefault.model_rebuild()
ClusterWorkloadDefault.model_rebuild()
ConnectionEventDefault.model_rebuild()
ConnectionEventInNodeDefault.model_rebuild()
ConversationDefault.model_rebuild()
ConversationInNodeDefault.model_rebuild()
CreateEdgeClusterResponseDefault.model_rebuild()
CreateEnrollmentTokenResponseDefault.model_rebuild()
CryptoTopupResultDefault.model_rebuild()
CryptoTopupStatusDefault.model_rebuild()
DVRChapterRef.model_rebuild()
DVRRequest.model_rebuild()
DeleteSuccessDefault.model_rebuild()
DeleteSuccess.model_rebuild()
DeveloperToken.model_rebuild()
EffectiveRetentionDefault.model_rebuild()
EventArtifact.model_rebuild()
EventMoney.model_rebuild()
FederationEventDefault.model_rebuild()
FederationSummaryDefault.model_rebuild()
GeographicDistributionDefault.model_rebuild()
IncidentDefault.model_rebuild()
IncidentDetailDefault.model_rebuild()
IncidentUpdatedEventDefault.model_rebuild()
InfrastructureNodeDefault.model_rebuild()
InfrastructureNodeInNodeDefault.model_rebuild()
IngestEndpoint.model_rebuild()
InvoiceDefault.model_rebuild()
InvoicePaymentDefault.model_rebuild()
LinkEmailPayloadDefault.model_rebuild()
MarketplaceClusterDefault.model_rebuild()
MediaCapacityConsentChangeDefault.model_rebuild()
MediaCapacityConsentDefault.model_rebuild()
MediaPlacementChangeDefault.model_rebuild()
MediaPlacementErrorInMediaCapacityConsentChangeResultDefault.model_rebuild()
MediaPlacementErrorInMediaCapacityConsentResultDefault.model_rebuild()
MediaPlacementErrorInMediaPlacementChangeResultDefault.model_rebuild()
MediaPlacementErrorInMediaPlacementOptionsResultDefault.model_rebuild()
MediaPlacementErrorInMediaPlacementPolicyResultDefault.model_rebuild()
MediaPlacementErrorInMediaPlacementPreviewResultDefault.model_rebuild()
MediaPlacementErrorInMediaPlacementReviewResultDefault.model_rebuild()
MediaPlacementOptionsConnectionDefault.model_rebuild()
MediaPlacementPolicyStateDefault.model_rebuild()
MediaPlacementPreviewDefault.model_rebuild()
MediaPlacementReviewDefault.model_rebuild()
MediaRetentionPolicyDefault.model_rebuild()
MessageDefault.model_rebuild()
MistAdminSessionDefault.model_rebuild()
MollieFirstPaymentDefault.model_rebuild()
MollieMandateDefault.model_rebuild()
MollieSubscriptionDefault.model_rebuild()
NetworkStatusDefault.model_rebuild()
NodeMetricDefault.model_rebuild()
NodeMetricHourlyDefault.model_rebuild()
NodeMetricsAggregatedDefault.model_rebuild()
NodePerformance5mDefault.model_rebuild()
NotFoundErrorDefault.model_rebuild()
NotFoundError.model_rebuild()
OrchestratorInstanceDefault.model_rebuild()
OrchestratorPerformancePointDefault.model_rebuild()
OrchestratorVantageDefault.model_rebuild()
OrchestratorWithDetailsDefault.model_rebuild()
OrchestratorsConnectionDefault.model_rebuild()
PageInfoDefault.model_rebuild()
PageInfo.model_rebuild()
PaymentDefault.model_rebuild()
PlatformOverviewDefault.model_rebuild()
PlayerBootSummaryDefault.model_rebuild()
PlayerBootTimeSeriesBucketDefault.model_rebuild()
PrepaidBalanceDefault.model_rebuild()
ProcessingUsageRecordDefault.model_rebuild()
ProcessingUsageRecordInNodeDefault.model_rebuild()
PromoteToPaidPayloadDefault.model_rebuild()
PullSourceEventDefault.model_rebuild()
PushTarget.model_rebuild()
QualityTierDailyDefault.model_rebuild()
RateLimitErrorDefault.model_rebuild()
RateLimitError.model_rebuild()
RebufferingEventDefault.model_rebuild()
RoutingEfficiencyDefault.model_rebuild()
RoutingEventDefault.model_rebuild()
ServiceInstanceDefault.model_rebuild()
ServiceInstanceHealthDefault.model_rebuild()
SessionQoeSummaryDefault.model_rebuild()
SessionQoeTimeSeriesBucketDefault.model_rebuild()
SigningKey.model_rebuild()
SigningKeyInNodeDefault.model_rebuild()
SkipperConversationDefault.model_rebuild()
SkipperConversationSummaryDefault.model_rebuild()
SkipperDoneDefault.model_rebuild()
SkipperMetaDefault.model_rebuild()
SkipperReportDefault.model_rebuild()
SkipperReportsConnectionDefault.model_rebuild()
SkipperTokenDefault.model_rebuild()
SkipperToolEndEventDefault.model_rebuild()
SkipperToolStartEventDefault.model_rebuild()
StorageArtifact.model_rebuild()
StorageEventDefault.model_rebuild()
StorageEventInNodeDefault.model_rebuild()
StorageUsageRecordDefault.model_rebuild()
StreamAnalyticsDailyDefault.model_rebuild()
StreamAnalyticsSummaryDefault.model_rebuild()
StreamConnectionHourlyDefault.model_rebuild()
StreamEventDefault.model_rebuild()
StreamEventInNodeDefault.model_rebuild()
Stream.model_rebuild()
StreamHealth5mDefault.model_rebuild()
StreamHealth5mInNodeDefault.model_rebuild()
StreamHealthMetricDefault.model_rebuild()
StreamHealthMetricInNodeDefault.model_rebuild()
StreamHealthSummaryDefault.model_rebuild()
StreamInNodeDefault.model_rebuild()
StreamKey.model_rebuild()
StreamMetrics.model_rebuild()
StreamRetentionOverridesDefault.model_rebuild()
StreamValidationDefault.model_rebuild()
StreamWithKey.model_rebuild()
StreamingConfigDefault.model_rebuild()
StripeBillingPortalSessionDefault.model_rebuild()
StripeCheckoutSessionDefault.model_rebuild()
SystemHealthEventDefault.model_rebuild()
TenantAnalyticsDailyDefault.model_rebuild()
TenantDailyStatDefault.model_rebuild()
TenantDefault.model_rebuild()
TenantEventDefault.model_rebuild()
TopAssetEntryDefault.model_rebuild()
TrackListEventDefault.model_rebuild()
TrackListEventInNodeDefault.model_rebuild()
TrackListUpdateDefault.model_rebuild()
ValidationErrorDefault.model_rebuild()
ValidationError.model_rebuild()
ViewerCountBucketDefault.model_rebuild()
ViewerEndpoint.model_rebuild()
ViewerGeoHourlyDefault.model_rebuild()
ViewerGeoHourlyInNodeDefault.model_rebuild()
ViewerGeographicDefault.model_rebuild()
ViewerHoursHourlyDefault.model_rebuild()
ViewerHoursHourlyInNodeDefault.model_rebuild()
ViewerMetricsDefault.model_rebuild()
ViewerSessionDefault.model_rebuild()
ViewerSessionInNodeDefault.model_rebuild()
VodAsset.model_rebuild()
VodAssetInNodeDefault.model_rebuild()
VodRetentionAssetDefault.model_rebuild()
VodRetentionDefault.model_rebuild()
WalletIdentityDefault.model_rebuild()
WalletLoginPayloadDefault.model_rebuild()
WebhookDeliveryDefault.model_rebuild()
WebhookEndpointDefault.model_rebuild()
WebhookEndpointSecretDefault.model_rebuild()
WebhookReplayResultDefault.model_rebuild()
WebhookTestResultDefault.model_rebuild()
X402PaymentResultDefault.model_rebuild()
