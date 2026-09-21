from datetime import datetime
from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .enums import (
    ClipCreationMode,
    DVRChapterMode,
    IngestMode,
    MonitoringToggle,
    PlaybackPolicyType,
    SortDirection,
    SourceLocationMode,
    StorageArtifactKind,
    StorageArtifactSortField,
)


class ConnectionInput(BaseModel):
    """Standard cursor-based pagination input for all connections.
    Follows the Relay Connection specification for consistent pagination.

    ## Forward Pagination
    Use `first` and `after` to paginate forward:
    ```graphql
    streamsConnection(page: { first: 10, after: "cursor..." })
    ```

    ## Backward Pagination
    Use `last` and `before` to paginate backward:
    ```graphql
    streamsConnection(page: { last: 10, before: "cursor..." })
    ```"""

    first: Optional[int] = Field(
        default=50,
        description="Number of items to fetch (forward pagination). Default: 50, Max: 500.",
    )
    "Number of items to fetch (forward pagination). Default: 50, Max: 500."
    after: Optional[str] = Field(
        default=None, description="Cursor to start fetching after (forward pagination)."
    )
    "Cursor to start fetching after (forward pagination)."
    last: Optional[int] = Field(
        default=None, description="Number of items to fetch (backward pagination)."
    )
    "Number of items to fetch (backward pagination)."
    before: Optional[str] = Field(
        default=None,
        description="Cursor to start fetching before (backward pagination).",
    )
    "Cursor to start fetching before (backward pagination)."


class PullSourceInput(BaseModel):
    source_uri: Optional[str] = Field(
        alias="sourceUri",
        default=None,
        description="Upstream RTSP, SRT, RIST, HLS, DTSC, or TS source URI. Required when creating or replacing a pull source.",
    )
    "Upstream RTSP, SRT, RIST, HLS, DTSC, or TS source URI. Required when creating or replacing a pull source."
    enabled: Optional[bool] = Field(
        default=True, description="Whether the media plane may pull from the source."
    )
    "Whether the media plane may pull from the source."


class SourceLocationClusterInput(BaseModel):
    cluster_id: str = Field(alias="clusterId")
    node_ids: list[str] = Field(
        alias="nodeIds",
        default_factory=lambda: [],
        description="Nodes of this cluster the source may run on. Empty means any node of the cluster. Only nodes of clusters the tenant owns are accepted.",
    )
    "Nodes of this cluster the source may run on. Empty means any node of the cluster. Only nodes of clusters the tenant owns are accepted."


class SourceLocationInput(BaseModel):
    """Replaces the stream's own ingest restriction. Private and multicast pull
    sources require RESTRICTED with clusters that allow private pull sources."""

    mode: SourceLocationMode = Field(
        description="ANY or RESTRICTED. CUSTOM is rejected."
    )
    "ANY or RESTRICTED. CUSTOM is rejected."
    clusters: list["SourceLocationClusterInput"] = Field(
        default_factory=lambda: [],
        description="Required and non-empty for RESTRICTED; must be empty for ANY.",
    )
    "Required and non-empty for RESTRICTED; must be empty for ANY."
    avoid_node_ids: list[str] = Field(
        alias="avoidNodeIds",
        default_factory=lambda: [],
        description="Nodes the source must never run on. Only nodes of clusters the tenant owns are accepted.",
    )
    "Nodes the source must never run on. Only nodes of clusters the tenant owns are accepted."


class CreateStreamInput(BaseModel):
    """Input for creating a new live stream."""

    name: str = Field(description="Human-readable name for the stream.")
    "Human-readable name for the stream."
    description: Optional[str] = Field(
        default=None, description="Optional description for the stream."
    )
    "Optional description for the stream."
    record: Optional[bool] = Field(
        default=False, description="Enable DVR recording (default: false)."
    )
    "Enable DVR recording (default: false)."
    ingest_mode: Optional[IngestMode] = Field(
        alias="ingestMode",
        default=IngestMode.PUSH,
        description="Source ingest model. Defaults to PUSH.",
    )
    "Source ingest model. Defaults to PUSH."
    pull_source: Optional["PullSourceInput"] = Field(
        alias="pullSource",
        default=None,
        description="Pull-source configuration. Required when ingestMode is PULL.",
    )
    "Pull-source configuration. Required when ingestMode is PULL."
    source_location: Optional["SourceLocationInput"] = Field(
        alias="sourceLocation",
        default=None,
        description="Where the source may be ingested. Omitted means ANY. Required for private and multicast pull sources.",
    )
    "Where the source may be ingested. Omitted means ANY. Required for private and multicast pull sources."


class UpdateStreamInput(BaseModel):
    """Input for updating an existing stream.
    All fields are optional - only provided fields are updated."""

    name: Optional[str] = Field(default=None, description="New name for the stream.")
    "New name for the stream."
    description: Optional[str] = Field(
        default=None, description="New description for the stream."
    )
    "New description for the stream."
    record: Optional[bool] = Field(
        default=None, description="Enable or disable DVR recording."
    )
    "Enable or disable DVR recording."
    ingest_mode: Optional[IngestMode] = Field(
        alias="ingestMode",
        default=None,
        description="Ingest model cannot be changed after create; sending a different value returns a validation error.",
    )
    "Ingest model cannot be changed after create; sending a different value returns a validation error."
    pull_source: Optional["PullSourceInput"] = Field(
        alias="pullSource",
        default=None,
        description="Update the pull-source configuration for an existing pull stream.",
    )
    "Update the pull-source configuration for an existing pull stream."
    source_location: Optional["SourceLocationInput"] = Field(
        alias="sourceLocation",
        default=None,
        description="Replace where the source may be ingested. Omitted keeps the current location. Rejected for managed streams.",
    )
    "Replace where the source may be ingested. Omitted keeps the current location. Rejected for managed streams."
    dvr_chapter_mode: Optional[DVRChapterMode] = Field(
        alias="dvrChapterMode",
        default=None,
        description="Historical chapter rotation mode. Snapshotted onto the DVR artifact\nat StartDVR; changes take effect on the next recording, not in-flight.\nNONE means rolling DVR playback only: recording still runs, but no\nfinalized chapter artifacts are produced for historical replay.",
    )
    "Historical chapter rotation mode. Snapshotted onto the DVR artifact\nat StartDVR; changes take effect on the next recording, not in-flight.\nNONE means rolling DVR playback only: recording still runs, but no\nfinalized chapter artifacts are produced for historical replay."
    dvr_chapter_interval_seconds: Optional[int] = Field(
        alias="dvrChapterIntervalSeconds",
        default=None,
        description="Chapter interval in seconds. Required when dvrChapterMode =\nFIXED_INTERVAL. Minimum 3600 (1 hour).",
    )
    "Chapter interval in seconds. Required when dvrChapterMode =\nFIXED_INTERVAL. Minimum 3600 (1 hour)."
    monitoring: Optional[MonitoringToggle] = Field(
        default=None,
        description="Per-stream Skipper monitoring override. INHERIT follows the tenant tier.",
    )
    "Per-stream Skipper monitoring override. INHERIT follows the tenant tier."


class CreateClipInput(BaseModel):
    """Input for creating a clip from a live stream's DVR buffer.
    Time specification depends on the selected mode."""

    stream_id: str = Field(
        alias="streamId",
        description="Stream to create the clip from (Stream.id, Relay global ID).",
    )
    "Stream to create the clip from (Stream.id, Relay global ID)."
    title: str = Field(description="Display title for the clip.")
    "Display title for the clip."
    description: Optional[str] = Field(
        default=None, description="Optional description."
    )
    "Optional description."
    mode: Optional[ClipCreationMode] = Field(
        default=None,
        description="Time mode (ABSOLUTE, RELATIVE, DURATION, CLIP_NOW). Default: ABSOLUTE.",
    )
    "Time mode (ABSOLUTE, RELATIVE, DURATION, CLIP_NOW). Default: ABSOLUTE."
    start_unix: Optional[int] = Field(
        alias="startUnix",
        default=None,
        description="Start time as Unix timestamp (ABSOLUTE/DURATION mode).",
    )
    "Start time as Unix timestamp (ABSOLUTE/DURATION mode)."
    stop_unix: Optional[int] = Field(
        alias="stopUnix",
        default=None,
        description="End time as Unix timestamp (ABSOLUTE mode).",
    )
    "End time as Unix timestamp (ABSOLUTE mode)."
    start_media: Optional[int] = Field(
        alias="startMedia",
        default=None,
        description="Start time as seconds from stream start (RELATIVE mode).",
    )
    "Start time as seconds from stream start (RELATIVE mode)."
    stop_media: Optional[int] = Field(
        alias="stopMedia",
        default=None,
        description="End time as seconds from stream start (RELATIVE mode).",
    )
    "End time as seconds from stream start (RELATIVE mode)."
    duration: Optional[int] = Field(
        default=None, description="Clip duration in seconds (DURATION/CLIP_NOW mode)."
    )
    "Clip duration in seconds (DURATION/CLIP_NOW mode)."
    expires_at: Optional[int] = Field(
        alias="expiresAt",
        default=None,
        description="Optional expiration as Unix timestamp.",
    )
    "Optional expiration as Unix timestamp."
    start_time: Optional[int] = Field(
        alias="startTime",
        default=None,
        description="Deprecated: Use startUnix instead.",
    )
    "Deprecated: Use startUnix instead."
    end_time: Optional[int] = Field(
        alias="endTime", default=None, description="Deprecated: Use stopUnix instead."
    )
    "Deprecated: Use stopUnix instead."


class CreateVodUploadInput(BaseModel):
    """Input for initiating a multipart VOD upload.
    Returns presigned S3 URLs for uploading file parts."""

    filename: str = Field(
        description="Original filename (for metadata and content-type detection)."
    )
    "Original filename (for metadata and content-type detection)."
    size_bytes: float = Field(
        alias="sizeBytes",
        description="Total file size in bytes (required for part calculation).",
    )
    "Total file size in bytes (required for part calculation)."
    content_type: Optional[str] = Field(
        alias="contentType",
        default=None,
        description="MIME type (video/mp4, video/webm, etc.). Auto-detected if omitted.",
    )
    "MIME type (video/mp4, video/webm, etc.). Auto-detected if omitted."
    title: Optional[str] = Field(
        default=None, description="Optional display title for the asset."
    )
    "Optional display title for the asset."
    description: Optional[str] = Field(
        default=None, description="Optional description for the asset."
    )
    "Optional description for the asset."


class CompleteVodUploadInput(BaseModel):
    """Input for completing a multipart VOD upload.
    Call after all parts have been uploaded to S3."""

    upload_id: str = Field(
        alias="uploadId", description="Upload session ID from createVodUpload."
    )
    "Upload session ID from createVodUpload."
    parts: list["VodUploadCompletedPart"] = Field(
        description="ETags from each successfully uploaded part."
    )
    "ETags from each successfully uploaded part."


class VodUploadCompletedPart(BaseModel):
    """Completed part info returned by S3 after each part upload."""

    part_number: int = Field(alias="partNumber", description="1-indexed part number.")
    "1-indexed part number."
    etag: str = Field(description="ETag header value returned by S3 on part upload.")
    "ETag header value returned by S3 on part upload."


class StorageArtifactsInput(BaseModel):
    first: Optional[int] = 25
    offset: Optional[int] = 0
    stream_id: Optional[str] = Field(alias="streamId", default=None)
    kinds: Optional[list[StorageArtifactKind]] = None
    search: Optional[str] = None
    status: Optional[str] = Field(
        default=None,
        description='Account-wide status filter applied server-side before count/facets/pagination: "ready" | "failed" | "processing" | "expired". Empty = all.',
    )
    'Account-wide status filter applied server-side before count/facets/pagination: "ready" | "failed" | "processing" | "expired". Empty = all.'
    artifact_hash: Optional[str] = Field(
        alias="artifactHash",
        default=None,
        description="Exact artifact-hash match — the canonical lookup for asset detail/analytics routes.\nTakes precedence over `search`; combine with `first: 1` to fetch a single artifact\nwithout a fuzzy scan or page cap.",
    )
    "Exact artifact-hash match — the canonical lookup for asset detail/analytics routes.\nTakes precedence over `search`; combine with `first: 1` to fetch a single artifact\nwithout a fuzzy scan or page cap."
    sort: Optional[StorageArtifactSortField] = StorageArtifactSortField.CREATED_AT
    direction: Optional[SortDirection] = SortDirection.DESC


class CreateDeveloperTokenInput(BaseModel):
    """Input for creating a developer API token."""

    name: str = Field(description="Human-readable name for the token.")
    "Human-readable name for the token."
    permissions: Optional[str] = Field(
        default=None,
        description="Comma-separated permission scopes (read:streams, write:streams, etc.).",
    )
    "Comma-separated permission scopes (read:streams, write:streams, etc.)."
    expires_in: Optional[int] = Field(
        alias="expiresIn",
        default=None,
        description="Days until expiration (null = non-expiring).",
    )
    "Days until expiration (null = non-expiring)."


class CreateStreamKeyInput(BaseModel):
    """Input for creating an additional stream key."""

    name: str = Field(description="Human-readable name for the key.")
    "Human-readable name for the key."


class TimeRangeInput(BaseModel):
    """Time range for filtering time-series data."""

    start: datetime = Field(description="Start of the time range.")
    "Start of the time range."
    end: datetime = Field(description="End of the time range.")
    "End of the time range."


class CreatePushTargetInput(BaseModel):
    platform: Optional[str] = Field(
        default=None,
        description="Platform identifier (twitch, youtube, facebook, kick, x, custom).",
    )
    "Platform identifier (twitch, youtube, facebook, kick, x, custom)."
    name: str = Field(description="User-friendly label for this target.")
    "User-friendly label for this target."
    target_uri: str = Field(
        alias="targetUri",
        description="Full target URI including stream key (e.g., rtmp://live.twitch.tv/app/live_xxxx).",
    )
    "Full target URI including stream key (e.g., rtmp://live.twitch.tv/app/live_xxxx)."


class UpdatePushTargetInput(BaseModel):
    name: Optional[str] = Field(default=None, description="Updated label.")
    "Updated label."
    target_uri: Optional[str] = Field(
        alias="targetUri", default=None, description="Updated target URI."
    )
    "Updated target URI."
    is_enabled: Optional[bool] = Field(
        alias="isEnabled", default=None, description="Enable or disable this target."
    )
    "Enable or disable this target."


class CreateSigningKeyInput(BaseModel):
    name: str = Field(
        description="Display label for the key (e.g. 'staging', 'rotation-2026-q2')."
    )
    "Display label for the key (e.g. 'staging', 'rotation-2026-q2')."


class SetPlaybackPolicyInput(BaseModel):
    stream_id: Optional[str] = Field(
        alias="streamId",
        default=None,
        description="Exactly one of streamId, vodAssetId, or clipId must be provided.",
    )
    "Exactly one of streamId, vodAssetId, or clipId must be provided."
    vod_asset_id: Optional[str] = Field(alias="vodAssetId", default=None)
    clip_id: Optional[str] = Field(alias="clipId", default=None)
    policy: "PlaybackPolicyInput"


class PlaybackPolicyInput(BaseModel):
    type_: PlaybackPolicyType = Field(alias="type")
    jwt: Optional["PlaybackJwtPolicyInput"] = Field(
        default=None, description="Required when type == JWT."
    )
    "Required when type == JWT."
    webhook: Optional["PlaybackWebhookPolicyInput"] = Field(
        default=None, description="Required when type == WEBHOOK."
    )
    "Required when type == WEBHOOK."


class PlaybackJwtPolicyInput(BaseModel):
    allowed_kids: Optional[list[str]] = Field(
        alias="allowedKids",
        default=None,
        description="Allowed signing key IDs. Empty = any active tenant key.",
    )
    "Allowed signing key IDs. Empty = any active tenant key."
    required_audience: Optional[list[str]] = Field(
        alias="requiredAudience",
        default=None,
        description="If set, the viewer JWT's `aud` claim must contain at least one of these.",
    )
    "If set, the viewer JWT's `aud` claim must contain at least one of these."
    required_claims_json: Optional[list["PlaybackJwtClaimRequirementInput"]] = Field(
        alias="requiredClaimsJson",
        default=None,
        description="Required claim constraints. Each entry's value is JSON-encoded for type flexibility.",
    )
    "Required claim constraints. Each entry's value is JSON-encoded for type flexibility."


class PlaybackJwtClaimRequirementInput(BaseModel):
    name: str
    json_value: str = Field(
        alias="jsonValue",
        description='JSON-encoded expected value (e.g. `"pro"`, `true`, `42`).',
    )
    'JSON-encoded expected value (e.g. `"pro"`, `true`, `42`).'


class PlaybackWebhookPolicyInput(BaseModel):
    url: str = Field(
        description="https URL FrameWorks will POST to on each viewer connect."
    )
    "https URL FrameWorks will POST to on each viewer connect."
    secret: Optional[str] = Field(
        default=None,
        description="HMAC-SHA256 secret used to sign outbound webhook bodies. Write-only:\npersisted via field-level encryption and never returned in queries.\nRequired when creating a webhook policy. Omit when updating an existing\nwebhook policy to keep the currently configured secret.",
    )
    "HMAC-SHA256 secret used to sign outbound webhook bodies. Write-only:\npersisted via field-level encryption and never returned in queries.\nRequired when creating a webhook policy. Omit when updating an existing\nwebhook policy to keep the currently configured secret."
    timeout_ms: Optional[int] = Field(
        alias="timeoutMs",
        default=None,
        description="Outbound POST timeout in milliseconds. Server caps at 10000; default 5000.",
    )
    "Outbound POST timeout in milliseconds. Server caps at 10000; default 5000."


class TestPlaybackAccessInput(BaseModel):
    """Inputs for testPlaybackAccess. Exactly one of playbackId / internalName must
    be set (the server rejects both-set and neither-set). viewerToken is
    required for JWT policies and is forwarded to the webhook payload for
    webhook policies. fireWebhook gates a real outbound HTTPS call to the
    customer URL. That call uses the same SSRF-hardened evaluator path as live
    viewer admission, and customer endpoints may log, rate-limit, or trigger
    side effects from the test request. When playbackId is supplied, the server resolves the
    canonical internalName before invoking the evaluator so webhook payloads
    carry the same streamName the live USER_NEW path would."""

    playback_id: Optional[str] = Field(alias="playbackId", default=None)
    internal_name: Optional[str] = Field(alias="internalName", default=None)
    viewer_token: Optional[str] = Field(
        alias="viewerToken",
        default=None,
        description="JWT to test (required for type=jwt; passed through in the webhook payload for type=webhook).",
    )
    "JWT to test (required for type=jwt; passed through in the webhook payload for type=webhook)."
    viewer_ip: Optional[str] = Field(alias="viewerIp", default=None)
    request_url: Optional[str] = Field(alias="requestUrl", default=None)
    connector: Optional[str] = None
    session_id: Optional[str] = Field(alias="sessionId", default=None)
    fire_webhook: Optional[bool] = Field(
        alias="fireWebhook",
        default=None,
        description='Webhook policies only: when true, Foghorn will POST to the configured\ncustomer URL with an HMAC-signed payload (same path the live evaluator\nuses). When false, the evaluator returns reason="webhook-test-skipped"\nwithout making the call so operators can inspect the resolved policy\nwithout side effects.',
    )
    'Webhook policies only: when true, Foghorn will POST to the configured\ncustomer URL with an HMAC-signed payload (same path the live evaluator\nuses). When false, the evaluator returns reason="webhook-test-skipped"\nwithout making the call so operators can inspect the resolved policy\nwithout side effects.'


SourceLocationInput.model_rebuild()
CreateStreamInput.model_rebuild()
UpdateStreamInput.model_rebuild()
CompleteVodUploadInput.model_rebuild()
SetPlaybackPolicyInput.model_rebuild()
PlaybackPolicyInput.model_rebuild()
PlaybackJwtPolicyInput.model_rebuild()
