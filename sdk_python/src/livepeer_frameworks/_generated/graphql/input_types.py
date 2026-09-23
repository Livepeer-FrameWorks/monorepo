from datetime import datetime
from typing import Any, Optional

from pydantic import Field

from .base_model import BaseModel
from .enums import (
    CardPaymentProvider,
    ClipCreationMode,
    ClusterPricingModel,
    ClusterVisibility,
    CryptoAsset,
    DVRChapterMode,
    IncidentStatus,
    IngestMode,
    MediaPlacementCharging,
    MediaPlacementClass,
    MediaPlacementOptionKind,
    MediaPlacementOrder,
    MediaPlacementScopeKind,
    MediaPlacementSpillover,
    MediaPlacementUpdateKind,
    MediaPlacementVerb,
    MediaRetentionTarget,
    MonitoringToggle,
    NodeOperationalMode,
    PaymentMethod,
    PlaybackPolicyType,
    SkipperMode,
    SortDirection,
    SourceLocationMode,
    StorageArtifactKind,
    StorageArtifactSortField,
)


class ApplyMediaCapacityConsentInput(BaseModel):
    cluster_id: str = Field(alias="clusterId")
    expected_revision: str = Field(alias="expectedRevision")
    allow_ingest: bool = Field(alias="allowIngest")
    allow_serve: bool = Field(alias="allowServe")
    allow_external_source: bool = Field(alias="allowExternalSource")
    review_token: str = Field(alias="reviewToken")
    idempotency_key: str = Field(alias="idempotencyKey")
    acknowledged_warning_ids: list[str] = Field(
        alias="acknowledgedWarningIds", default_factory=lambda: []
    )


class ApplyMediaPlacementChangeInput(BaseModel):
    scope: "MediaPlacementScopeInput"
    expected_revision: str = Field(alias="expectedRevision")
    expected_parent_revision: str = Field(alias="expectedParentRevision")
    updates: list["MediaPlacementVerbUpdateInput"]
    review_token: str = Field(alias="reviewToken")
    idempotency_key: str = Field(alias="idempotencyKey")
    acknowledged_warning_ids: list[str] = Field(
        alias="acknowledgedWarningIds", default_factory=lambda: []
    )


class BillingAddressInput(BaseModel):
    """Input for billing address."""

    street: str = Field(description="Street address line 1.")
    "Street address line 1."
    city: str = Field(description="City name.")
    "City name."
    state: Optional[str] = Field(default=None, description="State or province.")
    "State or province."
    postal_code: str = Field(alias="postalCode", description="Postal or ZIP code.")
    "Postal or ZIP code."
    country: str = Field(description="ISO 3166-1 alpha-2 country code.")
    "ISO 3166-1 alpha-2 country code."


class BootstrapEdgeInput(BaseModel):
    token: str = Field(
        description="Bootstrap token issued by createEdgeCluster or createEnrollmentToken."
    )
    "Bootstrap token issued by createEdgeCluster or createEnrollmentToken."
    external_ip: Optional[str] = Field(
        alias="externalIp",
        default=None,
        description="External IP of the edge being bootstrapped (helps Foghorn assign a domain).",
    )
    "External IP of the edge being bootstrapped (helps Foghorn assign a domain)."
    preferred_node_id: Optional[str] = Field(
        alias="preferredNodeId",
        default=None,
        description="Caller-supplied node id hint; Foghorn may ignore.",
    )
    "Caller-supplied node id hint; Foghorn may ignore."


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


class CreateCardTopupInput(BaseModel):
    """Input for creating a card-based prepaid balance top-up."""

    amount_cents: int = Field(
        alias="amountCents",
        description="Amount to charge, in cents of the tenant's presentment currency. Minimum 500 cents; maximum 10,000,000 cents.",
    )
    "Amount to charge, in cents of the tenant's presentment currency. Minimum 500 cents; maximum 10,000,000 cents."
    provider: CardPaymentProvider = Field(description="Payment provider to use.")
    "Payment provider to use."
    success_url: str = Field(
        alias="successUrl", description="URL to redirect after successful payment."
    )
    "URL to redirect after successful payment."
    cancel_url: str = Field(
        alias="cancelUrl", description="URL to redirect if user cancels."
    )
    "URL to redirect if user cancels."
    billing_email: Optional[str] = Field(
        alias="billingEmail",
        default=None,
        description="Optional billing email for invoice.",
    )
    "Optional billing email for invoice."
    billing_name: Optional[str] = Field(
        alias="billingName",
        default=None,
        description="Optional billing name for invoice.",
    )
    "Optional billing name for invoice."
    billing_company: Optional[str] = Field(
        alias="billingCompany",
        default=None,
        description="Optional company name for invoice.",
    )
    "Optional company name for invoice."
    billing_vat_number: Optional[str] = Field(
        alias="billingVatNumber",
        default=None,
        description="Optional VAT number for invoice.",
    )
    "Optional VAT number for invoice."


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


class CreateClusterInviteInput(BaseModel):
    """Input for creating a cluster access invite."""

    cluster_id: str = Field(alias="clusterId", description="Target cluster ID.")
    "Target cluster ID."
    invited_tenant_id: str = Field(
        alias="invitedTenantId", description="Tenant to invite."
    )
    "Tenant to invite."
    access_level: Optional[str] = Field(
        alias="accessLevel",
        default=None,
        description="Access level (read, write, admin).",
    )
    "Access level (read, write, admin)."
    resource_limits: Optional[Any] = Field(
        alias="resourceLimits",
        default=None,
        description="Resource limits for the invited tenant.",
    )
    "Resource limits for the invited tenant."
    expires_in_days: Optional[int] = Field(
        alias="expiresInDays", default=None, description="Days until invite expires."
    )
    "Days until invite expires."


class CreateConversationInput(BaseModel):
    """Input for creating a new conversation."""

    subject: Optional[str] = Field(default=None, description="Optional subject line.")
    "Optional subject line."
    message: str = Field(description="Initial message content (required).")
    "Initial message content (required)."
    page_url: Optional[str] = Field(
        alias="pageUrl",
        default=None,
        description="Optional page URL where conversation was initiated.",
    )
    "Optional page URL where conversation was initiated."


class CreateCryptoTopupInput(BaseModel):
    """Input for creating a crypto top-up deposit address."""

    amount_cents: int = Field(
        alias="amountCents",
        description="Amount in cents of the tenant's presentment currency. Minimum 1 cent; maximum 10,000,000 cents.",
    )
    "Amount in cents of the tenant's presentment currency. Minimum 1 cent; maximum 10,000,000 cents."
    asset: CryptoAsset = Field(
        description="Crypto asset to receive (ETH or USDC; LPT not yet supported)."
    )
    "Crypto asset to receive (ETH or USDC; LPT not yet supported)."


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


class CreateEdgeClusterInput(BaseModel):
    cluster_name: str = Field(
        alias="clusterName", description="Human-readable cluster name."
    )
    "Human-readable cluster name."
    short_description: Optional[str] = Field(
        alias="shortDescription", default=None, description="Short description."
    )
    "Short description."
    control_cluster_id: Optional[str] = Field(
        alias="controlClusterId",
        default=None,
        description="Optional platform-official cluster/cell to control this edge cluster.",
    )
    "Optional platform-official cluster/cell to control this edge cluster."


class CreatePaymentInput(BaseModel):
    invoice_id: str = Field(alias="invoiceId")
    method: PaymentMethod
    return_url: Optional[str] = Field(alias="returnUrl", default=None)


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


class CreateSigningKeyInput(BaseModel):
    name: str = Field(
        description="Display label for the key (e.g. 'staging', 'rotation-2026-q2')."
    )
    "Display label for the key (e.g. 'staging', 'rotation-2026-q2')."


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


class CreateStreamKeyInput(BaseModel):
    """Input for creating an additional stream key."""

    name: str = Field(description="Human-readable name for the key.")
    "Human-readable name for the key."


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


class CreateWebhookEndpointInput(BaseModel):
    url: str = Field(description="An https URL of a public host.")
    "An https URL of a public host."
    description: Optional[str] = None
    event_types: list[str] = Field(
        alias="eventTypes",
        description='Public event types to receive; "*" receives every type. See webhookEventTypes.',
    )
    'Public event types to receive; "*" receives every type. See webhookEventTypes.'
    api_version: Optional[str] = Field(
        alias="apiVersion",
        default=None,
        description='Public event package version; defaults to "v1".',
    )
    'Public event package version; defaults to "v1".'


class IncidentFilterInput(BaseModel):
    statuses: Optional[list[IncidentStatus]] = Field(
        default=None, description="Empty or omitted matches every status."
    )
    "Empty or omitted matches every status."
    cluster_id: Optional[str] = Field(alias="clusterId", default=None)


class LinkEmailInput(BaseModel):
    """Input for linking email to a wallet-only account."""

    email: str = Field(description="Email address to link.")
    "Email address to link."
    password: str = Field(description="Password to set for email-based login.")
    "Password to set for email-based login."


class MediaPlacementAllowInput(BaseModel):
    any: list["MediaPlacementSelectorInput"]


class MediaPlacementConstraintsInput(BaseModel):
    allow: Optional["MediaPlacementAllowInput"] = Field(
        default=None,
        description="Omitted means no additional restriction; an explicit empty any list allows nothing.",
    )
    "Omitted means no additional restriction; an explicit empty any list allows nothing."
    deny: list["MediaPlacementSelectorInput"] = Field(default_factory=lambda: [])


class MediaPlacementCoordinatesInput(BaseModel):
    latitude: float
    longitude: float


class MediaPlacementGroupInput(BaseModel):
    id: str
    match: "MediaPlacementSelectorInput"
    order: MediaPlacementOrder = MediaPlacementOrder.DISTANCE
    spillover: MediaPlacementSpillover = MediaPlacementSpillover.NEVER
    max_distance_km: float = Field(
        alias="maxDistanceKm",
        default=0,
        description="Hard maximum distance in km. Zero means unbounded.",
    )
    "Hard maximum distance in km. Zero means unbounded."
    geo_hole_distance_km: float = Field(
        alias="geoHoleDistanceKm",
        default=0,
        description="Soft threshold for geographic spill; must be positive for GEO_HOLE modes.",
    )
    "Soft threshold for geographic spill; must be positive for GEO_HOLE modes."
    min_improvement_km: float = Field(alias="minImprovementKm", default=0)
    price_currency: Optional[str] = Field(alias="priceCurrency", default=None)
    price_unit: Optional[str] = Field(alias="priceUnit", default=None)


class MediaPlacementOptionsFilter(BaseModel):
    query: Optional[str] = None
    kind: Optional[MediaPlacementOptionKind] = None
    classes: Optional[list[MediaPlacementClass]] = None
    cluster_id: Optional[str] = Field(
        alias="clusterId",
        default=None,
        description="Limits NODE options to one cluster.",
    )
    "Limits NODE options to one cluster."


class MediaPlacementPreferencesInput(BaseModel):
    groups: list["MediaPlacementGroupInput"]


class MediaPlacementRulesInput(BaseModel):
    schema_version: int = Field(alias="schemaVersion", default=1)
    constraints: "MediaPlacementConstraintsInput"
    preferences: Optional["MediaPlacementPreferencesInput"] = Field(
        default=None,
        description="Omitted inherits preference order; an explicit empty groups list denies all destinations.",
    )
    "Omitted inherits preference order; an explicit empty groups list denies all destinations."


class MediaPlacementScopeInput(BaseModel):
    kind: MediaPlacementScopeKind
    stream_id: Optional[str] = Field(alias="streamId", default=None)


class MediaPlacementSelectorInput(BaseModel):
    """Fields combine with AND; values within a field combine with OR. Empty matches all entitled capacity."""

    cluster_ids: Optional[list[str]] = Field(alias="clusterIds", default=None)
    node_ids: Optional[list[str]] = Field(
        alias="nodeIds", default=None, description="Nodes of clusters the tenant owns."
    )
    "Nodes of clusters the tenant owns."
    owner_ids: Optional[list[str]] = Field(alias="ownerIds", default=None)
    regions: Optional[list[str]] = None
    classes: Optional[list[MediaPlacementClass]] = None
    charging: Optional[list[MediaPlacementCharging]] = None


class MediaPlacementVerbUpdateInput(BaseModel):
    verb: MediaPlacementVerb
    kind: MediaPlacementUpdateKind
    rules: Optional["MediaPlacementRulesInput"] = None


class OpenMistAdminSessionInput(BaseModel):
    node_id: str = Field(
        alias="nodeId",
        description="Node identifier — accepts the InfrastructureNode.id (Relay global) or nodeId (raw UUID).",
    )
    "Node identifier — accepts the InfrastructureNode.id (Relay global) or nodeId (raw UUID)."


class PlaybackJwtClaimRequirementInput(BaseModel):
    name: str
    json_value: str = Field(
        alias="jsonValue",
        description='JSON-encoded expected value (e.g. `"pro"`, `true`, `42`).',
    )
    'JSON-encoded expected value (e.g. `"pro"`, `true`, `42`).'


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


class PreviewMediaPlacementInput(BaseModel):
    scope: "MediaPlacementScopeInput"
    verb: MediaPlacementVerb
    stream_id: Optional[str] = Field(alias="streamId", default=None)
    protocol: Optional[str] = None
    coordinates: Optional["MediaPlacementCoordinatesInput"] = None
    draft_update: Optional["MediaPlacementVerbUpdateInput"] = Field(
        alias="draftUpdate",
        default=None,
        description="Omitted evaluates saved rules; CLEAR previews inheritance for this verb.",
    )
    "Omitted evaluates saved rules; CLEAR previews inheritance for this verb."
    expected_revision: Optional[str] = Field(alias="expectedRevision", default=None)
    expected_parent_revision: Optional[str] = Field(
        alias="expectedParentRevision", default=None
    )


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


class ResetMediaRetentionOverrideInput(BaseModel):
    target_type: MediaRetentionTarget = Field(alias="targetType")
    target_id: str = Field(alias="targetId")


class ReviewMediaCapacityConsentInput(BaseModel):
    cluster_id: str = Field(alias="clusterId")
    expected_revision: str = Field(alias="expectedRevision")
    allow_ingest: bool = Field(alias="allowIngest")
    allow_serve: bool = Field(alias="allowServe")
    allow_external_source: bool = Field(alias="allowExternalSource")


class ReviewMediaPlacementChangeInput(BaseModel):
    scope: "MediaPlacementScopeInput"
    expected_revision: str = Field(alias="expectedRevision")
    expected_parent_revision: str = Field(alias="expectedParentRevision")
    updates: list["MediaPlacementVerbUpdateInput"]


class SendMessageInput(BaseModel):
    """Input for sending a message."""

    conversation_id: str = Field(
        alias="conversationId",
        description="The conversation ID to send the message to.",
    )
    "The conversation ID to send the message to."
    content: str = Field(description="The message content.")
    "The message content."


class SetMediaRetentionPolicyInput(BaseModel):
    target_type: MediaRetentionTarget = Field(
        alias="targetType", description="Which class to write: VOD, DVR, or CLIP."
    )
    "Which class to write: VOD, DVR, or CLIP."
    days: Optional[int] = Field(
        default=None,
        description="Days to retain new artifacts of this class. Range: 0 ≤ value ≤ tier\ncap (tier cap of 0 means uncapped). 0 = no auto-expire (only honored\non uncapped tiers; Free clamps to the cap at write time). Ignored\nwhen clear = true.",
    )
    "Days to retain new artifacts of this class. Range: 0 ≤ value ≤ tier\ncap (tier cap of 0 means uncapped). 0 = no auto-expire (only honored\non uncapped tiers; Free clamps to the cap at write time). Ignored\nwhen clear = true."
    clear: Optional[bool] = Field(
        default=None,
        description="When true, NULLs out the per-class column so the tenant inherits the\nsystem default (VOD: keep forever, DVR/clip: 30d). days is ignored.",
    )
    "When true, NULLs out the per-class column so the tenant inherits the\nsystem default (VOD: keep forever, DVR/clip: 30d). days is ignored."


class SetNodeModeInput(BaseModel):
    node_id: str = Field(
        alias="nodeId",
        description="Node identifier — accepts the InfrastructureNode.id (Relay global) or nodeId (raw UUID).",
    )
    "Node identifier — accepts the InfrastructureNode.id (Relay global) or nodeId (raw UUID)."
    mode: NodeOperationalMode
    reason: Optional[str] = Field(
        default=None,
        description="Free-text reason recorded in Foghorn's audit trail. Defaults to the calling user/agent identity.",
    )
    "Free-text reason recorded in Foghorn's audit trail. Defaults to the calling user/agent identity."


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


class SetStreamRetentionOverridesInput(BaseModel):
    stream_id: str = Field(alias="streamId")
    dvr_retention_days_override: Optional[int] = Field(
        alias="dvrRetentionDaysOverride",
        default=None,
        description="Per-class override values. Unset on a field leaves the column alone;\nsetting a value writes it (0 = no auto-expire, >0 = days). To remove\nan existing override and fall back to the tenant default, set the\nmatching clear*Override flag.",
    )
    "Per-class override values. Unset on a field leaves the column alone;\nsetting a value writes it (0 = no auto-expire, >0 = days). To remove\nan existing override and fall back to the tenant default, set the\nmatching clear*Override flag."
    clip_retention_days_override: Optional[int] = Field(
        alias="clipRetentionDaysOverride", default=None
    )
    clear_dvr_retention_override: Optional[bool] = Field(
        alias="clearDvrRetentionOverride", default=None
    )
    clear_clip_retention_override: Optional[bool] = Field(
        alias="clearClipRetentionOverride", default=None
    )


class SkipperChatInput(BaseModel):
    conversation_id: Optional[str] = Field(alias="conversationId", default=None)
    message: str
    page_url: Optional[str] = Field(alias="pageUrl", default=None)
    mode: Optional[SkipperMode] = None


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


class TimeRangeInput(BaseModel):
    """Time range for filtering time-series data."""

    start: datetime = Field(description="Start of the time range.")
    "Start of the time range."
    end: datetime = Field(description="End of the time range.")
    "End of the time range."


class UpdateBillingDetailsInput(BaseModel):
    """Input for updating billing details."""

    email: Optional[str] = Field(default=None, description="Billing contact email.")
    "Billing contact email."
    name: Optional[str] = Field(
        default=None,
        description="Customer legal or person name shown on full invoices.",
    )
    "Customer legal or person name shown on full invoices."
    company: Optional[str] = Field(
        default=None, description="Company name for invoices."
    )
    "Company name for invoices."
    vat_number: Optional[str] = Field(
        alias="vatNumber",
        default=None,
        description="VAT number (EU format: XX123456789).",
    )
    "VAT number (EU format: XX123456789)."
    address: Optional["BillingAddressInput"] = Field(
        default=None, description="Structured billing address."
    )
    "Structured billing address."


class UpdateClusterMarketplaceInput(BaseModel):
    """Input for updating cluster marketplace settings."""

    visibility: Optional[ClusterVisibility] = Field(
        default=None, description="Marketplace visibility (PUBLIC, UNLISTED, PRIVATE)."
    )
    "Marketplace visibility (PUBLIC, UNLISTED, PRIVATE)."
    pricing_model: Optional[ClusterPricingModel] = Field(
        alias="pricingModel",
        default=None,
        description="Pricing model for subscriptions.",
    )
    "Pricing model for subscriptions."
    monthly_price_cents: Optional[int] = Field(
        alias="monthlyPriceCents",
        default=None,
        description="Monthly subscription price in cents.",
    )
    "Monthly subscription price in cents."
    requires_approval: Optional[bool] = Field(
        alias="requiresApproval",
        default=None,
        description="Whether access requires owner approval.",
    )
    "Whether access requires owner approval."
    short_description: Optional[str] = Field(
        alias="shortDescription",
        default=None,
        description="Short marketplace description.",
    )
    "Short marketplace description."


class UpdateMediaRetentionInput(BaseModel):
    target_type: MediaRetentionTarget = Field(alias="targetType")
    target_id: str = Field(
        alias="targetId",
        description="Canonical asset ID — accepts either the asset's UUID or its hash.",
    )
    "Canonical asset ID — accepts either the asset's UUID or its hash."
    retention_days: Optional[int] = Field(
        alias="retentionDays",
        default=None,
        description="Either retentionDays (relative to NOW) or retentionUntil must be set.",
    )
    "Either retentionDays (relative to NOW) or retentionUntil must be set."
    retention_until: Optional[datetime] = Field(alias="retentionUntil", default=None)


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


class UpdateTenantInput(BaseModel):
    """Input for updating tenant settings."""

    name: Optional[str] = Field(default=None, description="New tenant name.")
    "New tenant name."
    settings: Optional[Any] = Field(
        default=None,
        description="Custom settings JSON. Supports primaryClusterId, subdomain, customDomain, and deploymentModel.",
    )
    "Custom settings JSON. Supports primaryClusterId, subdomain, customDomain, and deploymentModel."
    custom_domain: Optional[str] = Field(
        alias="customDomain",
        default=None,
        description="BYO domain. Empty string clears it; null leaves it unchanged. Navigator\npicks up the change on the next reconciler tick and starts/teardowns the\nverification + cert lifecycle accordingly.",
    )
    "BYO domain. Empty string clears it; null leaves it unchanged. Navigator\npicks up the change on the next reconciler tick and starts/teardowns the\nverification + cert lifecycle accordingly."
    monitoring_enabled: Optional[bool] = Field(
        alias="monitoringEnabled",
        default=None,
        description="Tenant-wide Skipper AI monitoring master switch. null leaves it unchanged.",
    )
    "Tenant-wide Skipper AI monitoring master switch. null leaves it unchanged."


class UpdateWebhookEndpointInput(BaseModel):
    url: Optional[str] = None
    description: Optional[str] = None
    event_types: Optional[list[str]] = Field(
        alias="eventTypes",
        default=None,
        description="Replaces the subscribed event types when set.",
    )
    "Replaces the subscribed event types when set."


class VodUploadCompletedPart(BaseModel):
    """Completed part info returned by S3 after each part upload."""

    part_number: int = Field(alias="partNumber", description="1-indexed part number.")
    "1-indexed part number."
    etag: str = Field(description="ETag header value returned by S3 on part upload.")
    "ETag header value returned by S3 on part upload."


class WalletLoginInput(BaseModel):
    """Input for wallet-based authentication.
    The signature proves ownership of the wallet address."""

    address: str = Field(
        description="Ethereum address (0x-prefixed, 40 hex characters)."
    )
    "Ethereum address (0x-prefixed, 40 hex characters)."
    message: str = Field(
        description="Message that was signed, including timestamp and nonce for replay protection."
    )
    "Message that was signed, including timestamp and nonce for replay protection."
    signature: str = Field(
        description="EIP-191 personal_sign signature (0x-prefixed, 65 bytes hex)."
    )
    "EIP-191 personal_sign signature (0x-prefixed, 65 bytes hex)."


ApplyMediaPlacementChangeInput.model_rebuild()
CompleteVodUploadInput.model_rebuild()
CreateStreamInput.model_rebuild()
MediaPlacementAllowInput.model_rebuild()
MediaPlacementConstraintsInput.model_rebuild()
MediaPlacementGroupInput.model_rebuild()
MediaPlacementPreferencesInput.model_rebuild()
MediaPlacementRulesInput.model_rebuild()
MediaPlacementVerbUpdateInput.model_rebuild()
PlaybackJwtPolicyInput.model_rebuild()
PlaybackPolicyInput.model_rebuild()
PreviewMediaPlacementInput.model_rebuild()
ReviewMediaPlacementChangeInput.model_rebuild()
SetPlaybackPolicyInput.model_rebuild()
SourceLocationInput.model_rebuild()
UpdateBillingDetailsInput.model_rebuild()
UpdateStreamInput.model_rebuild()
