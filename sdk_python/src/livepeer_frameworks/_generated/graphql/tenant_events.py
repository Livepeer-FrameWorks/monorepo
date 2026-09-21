from datetime import datetime
from typing import Annotated, Literal, Optional, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .enums import (
    EventCustomDomainFailureReason,
    EventIngestProtocol,
    EventMediaFailureReason,
    EventMultistreamStatus,
    EventPaymentFailureReason,
    EventSuspensionReason,
)
from .fragments import EventArtifact, EventMoney


class TenantEvents(BaseModel):
    """Real-time subscriptions for live event streaming via WebSocket.
    All subscriptions are tenant-scoped and require authentication.
    Events are delivered as they occur with minimal latency."""

    tenant_events: "TenantEventsTenantEvents" = Field(
        alias="tenantEvents",
        description='Public events of the current tenant, as webhooks deliver them: stream\nlifecycle, clip, recording, and upload lifecycle, multistream status, API\ntokens, billing, account, and custom domain events. Each event\'s data is the\nregistered payload of its type. Pass types to receive only those event types\n(e.g. ["clip.ready", "clip.failed"]); pass streamId to receive only events\nwhose payload names that stream.',
    )
    'Public events of the current tenant, as webhooks deliver them: stream\nlifecycle, clip, recording, and upload lifecycle, multistream status, API\ntokens, billing, account, and custom domain events. Each event\'s data is the\nregistered payload of its type. Pass types to receive only those event types\n(e.g. ["clip.ready", "clip.failed"]); pass streamId to receive only events\nwhose payload names that stream.'


class TenantEventsTenantEvents(BaseModel):
    """A public tenant event as its owning service emitted it. data is the registered
    message of type, unchanged; webhooks deliver the same message."""

    id: str = Field(description="Event ID, stable across redeliveries.")
    "Event ID, stable across redeliveries."
    type_: str = Field(
        alias="type", description="Registered event type, e.g. stream.live."
    )
    "Registered event type, e.g. stream.live."
    time: datetime = Field(description="When the state change committed.")
    "When the state change committed."
    subject: str = Field(
        description="The aggregate the event belongs to, as <aggregate>/<id>, e.g. streams/<stream id>."
    )
    "The aggregate the event belongs to, as <aggregate>/<id>, e.g. streams/<stream id>."
    data: Annotated[
        Union[
            "TenantEventsTenantEventsDataAccountSuspended",
            "TenantEventsTenantEventsDataApiTokenCreated",
            "TenantEventsTenantEventsDataApiTokenRevoked",
            "TenantEventsTenantEventsDataBillingDetailsUpdated",
            "TenantEventsTenantEventsDataInvoiceCreated",
            "TenantEventsTenantEventsDataInvoicePaid",
            "TenantEventsTenantEventsDataPaymentFailed",
            "TenantEventsTenantEventsDataTopupCredited",
            "TenantEventsTenantEventsDataClipFailed",
            "TenantEventsTenantEventsDataClipReady",
            "TenantEventsTenantEventsDataClipRequested",
            "TenantEventsTenantEventsDataCustomDomainFailed",
            "TenantEventsTenantEventsDataCustomDomainVerified",
            "TenantEventsTenantEventsDataMultistreamStatusChanged",
            "TenantEventsTenantEventsDataRecordingFailed",
            "TenantEventsTenantEventsDataRecordingReady",
            "TenantEventsTenantEventsDataStreamConnected",
            "TenantEventsTenantEventsDataStreamCreated",
            "TenantEventsTenantEventsDataStreamDeleted",
            "TenantEventsTenantEventsDataStreamIdle",
            "TenantEventsTenantEventsDataStreamKeyRotated",
            "TenantEventsTenantEventsDataStreamLive",
            "TenantEventsTenantEventsDataStreamUpdated",
            "TenantEventsTenantEventsDataUploadAborted",
            "TenantEventsTenantEventsDataUploadCompleted",
            "TenantEventsTenantEventsDataUploadCreated",
            "TenantEventsTenantEventsDataUploadFailed",
            "TenantEventsTenantEventsDataUploadReady",
            UnknownMember,
        ],
        OpenUnion(),
    ]


class TenantEventsTenantEventsDataAccountSuspended(BaseModel):
    """Payload of a PublicEvent: one member per public event type. Fields of the same
    name can have different types across members (reason is a different enum per
    event family); alias them when one selection covers several."""

    typename__: Literal["AccountSuspended"] = Field(alias="__typename")
    suspension_reason: EventSuspensionReason = Field(alias="suspensionReason")


class TenantEventsTenantEventsDataApiTokenCreated(BaseModel):
    """Payload of a PublicEvent: one member per public event type. Fields of the same
    name can have different types across members (reason is a different enum per
    event family); alias them when one selection covers several."""

    typename__: Literal["ApiTokenCreated"] = Field(alias="__typename")
    token_id: str = Field(alias="tokenId")
    name: str
    permissions: list[str]
    expires_at: Optional[datetime] = Field(alias="expiresAt")


class TenantEventsTenantEventsDataApiTokenRevoked(BaseModel):
    """Payload of a PublicEvent: one member per public event type. Fields of the same
    name can have different types across members (reason is a different enum per
    event family); alias them when one selection covers several."""

    typename__: Literal["ApiTokenRevoked"] = Field(alias="__typename")
    token_id: str = Field(alias="tokenId")


class TenantEventsTenantEventsDataBillingDetailsUpdated(BaseModel):
    """Payload of a PublicEvent: one member per public event type. Fields of the same
    name can have different types across members (reason is a different enum per
    event family); alias them when one selection covers several."""

    typename__: Literal["BillingDetailsUpdated"] = Field(alias="__typename")
    changed_fields: list[str] = Field(alias="changedFields")


class TenantEventsTenantEventsDataInvoiceCreated(BaseModel):
    """Payload of a PublicEvent: one member per public event type. Fields of the same
    name can have different types across members (reason is a different enum per
    event family); alias them when one selection covers several."""

    typename__: Literal["InvoiceCreated"] = Field(alias="__typename")
    invoice_id: str = Field(alias="invoiceId")
    amount_due: Optional["TenantEventsTenantEventsDataInvoiceCreatedAmountDue"] = Field(
        alias="amountDue"
    )
    period_start: Optional[datetime] = Field(alias="periodStart")
    period_end: Optional[datetime] = Field(alias="periodEnd")
    due_at: Optional[datetime] = Field(alias="dueAt")


class TenantEventsTenantEventsDataInvoiceCreatedAmountDue(EventMoney):
    """Part of a public event payload (frameworks.events.public.v1.Money)."""

    pass


class TenantEventsTenantEventsDataInvoicePaid(BaseModel):
    """Payload of a PublicEvent: one member per public event type. Fields of the same
    name can have different types across members (reason is a different enum per
    event family); alias them when one selection covers several."""

    typename__: Literal["InvoicePaid"] = Field(alias="__typename")
    invoice_id: str = Field(alias="invoiceId")
    amount_paid: Optional["TenantEventsTenantEventsDataInvoicePaidAmountPaid"] = Field(
        alias="amountPaid"
    )


class TenantEventsTenantEventsDataInvoicePaidAmountPaid(EventMoney):
    """Part of a public event payload (frameworks.events.public.v1.Money)."""

    pass


class TenantEventsTenantEventsDataPaymentFailed(BaseModel):
    """Payload of a PublicEvent: one member per public event type. Fields of the same
    name can have different types across members (reason is a different enum per
    event family); alias them when one selection covers several."""

    typename__: Literal["PaymentFailed"] = Field(alias="__typename")
    payment_id: str = Field(alias="paymentId")
    invoice_id: str = Field(alias="invoiceId")
    amount: Optional["TenantEventsTenantEventsDataPaymentFailedAmount"]
    payment_failure_reason: EventPaymentFailureReason = Field(
        alias="paymentFailureReason"
    )
    provider: str
    provider_reference_id: str = Field(alias="providerReferenceId")


class TenantEventsTenantEventsDataPaymentFailedAmount(EventMoney):
    """Part of a public event payload (frameworks.events.public.v1.Money)."""

    pass


class TenantEventsTenantEventsDataTopupCredited(BaseModel):
    """Payload of a PublicEvent: one member per public event type. Fields of the same
    name can have different types across members (reason is a different enum per
    event family); alias them when one selection covers several."""

    typename__: Literal["TopupCredited"] = Field(alias="__typename")
    topup_id: str = Field(alias="topupId")
    amount: Optional["TenantEventsTenantEventsDataTopupCreditedAmount"]


class TenantEventsTenantEventsDataTopupCreditedAmount(EventMoney):
    """Part of a public event payload (frameworks.events.public.v1.Money)."""

    pass


class TenantEventsTenantEventsDataClipFailed(BaseModel):
    """Payload of a PublicEvent: one member per public event type. Fields of the same
    name can have different types across members (reason is a different enum per
    event family); alias them when one selection covers several."""

    typename__: Literal["ClipFailed"] = Field(alias="__typename")
    artifact: Optional["TenantEventsTenantEventsDataClipFailedArtifact"]
    media_failure_reason: EventMediaFailureReason = Field(alias="mediaFailureReason")


class TenantEventsTenantEventsDataClipFailedArtifact(EventArtifact):
    """Part of a public event payload (frameworks.events.public.v1.Artifact)."""

    pass


class TenantEventsTenantEventsDataClipReady(BaseModel):
    """Payload of a PublicEvent: one member per public event type. Fields of the same
    name can have different types across members (reason is a different enum per
    event family); alias them when one selection covers several."""

    typename__: Literal["ClipReady"] = Field(alias="__typename")
    artifact: Optional["TenantEventsTenantEventsDataClipReadyArtifact"]
    duration_ms: int = Field(alias="durationMs")
    size_bytes: int = Field(alias="sizeBytes")


class TenantEventsTenantEventsDataClipReadyArtifact(EventArtifact):
    """Part of a public event payload (frameworks.events.public.v1.Artifact)."""

    pass


class TenantEventsTenantEventsDataClipRequested(BaseModel):
    """Payload of a PublicEvent: one member per public event type. Fields of the same
    name can have different types across members (reason is a different enum per
    event family); alias them when one selection covers several."""

    typename__: Literal["ClipRequested"] = Field(alias="__typename")
    artifact: Optional["TenantEventsTenantEventsDataClipRequestedArtifact"]
    duration_ms: int = Field(alias="durationMs")


class TenantEventsTenantEventsDataClipRequestedArtifact(EventArtifact):
    """Part of a public event payload (frameworks.events.public.v1.Artifact)."""

    pass


class TenantEventsTenantEventsDataCustomDomainFailed(BaseModel):
    """Payload of a PublicEvent: one member per public event type. Fields of the same
    name can have different types across members (reason is a different enum per
    event family); alias them when one selection covers several."""

    typename__: Literal["CustomDomainFailed"] = Field(alias="__typename")
    domain: str
    custom_domain_failure_reason: EventCustomDomainFailureReason = Field(
        alias="customDomainFailureReason"
    )


class TenantEventsTenantEventsDataCustomDomainVerified(BaseModel):
    """Payload of a PublicEvent: one member per public event type. Fields of the same
    name can have different types across members (reason is a different enum per
    event family); alias them when one selection covers several."""

    typename__: Literal["CustomDomainVerified"] = Field(alias="__typename")
    domain: str


class TenantEventsTenantEventsDataMultistreamStatusChanged(BaseModel):
    """Payload of a PublicEvent: one member per public event type. Fields of the same
    name can have different types across members (reason is a different enum per
    event family); alias them when one selection covers several."""

    typename__: Literal["MultistreamStatusChanged"] = Field(alias="__typename")
    stream_id: str = Field(alias="streamId")
    target_id: str = Field(alias="targetId")
    target_name: str = Field(alias="targetName")
    status: EventMultistreamStatus
    previous_status: EventMultistreamStatus = Field(alias="previousStatus")


class TenantEventsTenantEventsDataRecordingFailed(BaseModel):
    """Payload of a PublicEvent: one member per public event type. Fields of the same
    name can have different types across members (reason is a different enum per
    event family); alias them when one selection covers several."""

    typename__: Literal["RecordingFailed"] = Field(alias="__typename")
    artifact: Optional["TenantEventsTenantEventsDataRecordingFailedArtifact"]
    media_failure_reason: EventMediaFailureReason = Field(alias="mediaFailureReason")


class TenantEventsTenantEventsDataRecordingFailedArtifact(EventArtifact):
    """Part of a public event payload (frameworks.events.public.v1.Artifact)."""

    pass


class TenantEventsTenantEventsDataRecordingReady(BaseModel):
    """Payload of a PublicEvent: one member per public event type. Fields of the same
    name can have different types across members (reason is a different enum per
    event family); alias them when one selection covers several."""

    typename__: Literal["RecordingReady"] = Field(alias="__typename")
    artifact: Optional["TenantEventsTenantEventsDataRecordingReadyArtifact"]
    duration_ms: int = Field(alias="durationMs")
    size_bytes: int = Field(alias="sizeBytes")


class TenantEventsTenantEventsDataRecordingReadyArtifact(EventArtifact):
    """Part of a public event payload (frameworks.events.public.v1.Artifact)."""

    pass


class TenantEventsTenantEventsDataStreamConnected(BaseModel):
    """Payload of a PublicEvent: one member per public event type. Fields of the same
    name can have different types across members (reason is a different enum per
    event family); alias them when one selection covers several."""

    typename__: Literal["StreamConnected"] = Field(alias="__typename")
    stream_id: str = Field(alias="streamId")
    protocol: EventIngestProtocol


class TenantEventsTenantEventsDataStreamCreated(BaseModel):
    """Payload of a PublicEvent: one member per public event type. Fields of the same
    name can have different types across members (reason is a different enum per
    event family); alias them when one selection covers several."""

    typename__: Literal["StreamCreated"] = Field(alias="__typename")
    stream_id: str = Field(alias="streamId")
    name: str
    playback_id: str = Field(alias="playbackId")


class TenantEventsTenantEventsDataStreamDeleted(BaseModel):
    """Payload of a PublicEvent: one member per public event type. Fields of the same
    name can have different types across members (reason is a different enum per
    event family); alias them when one selection covers several."""

    typename__: Literal["StreamDeleted"] = Field(alias="__typename")
    stream_id: str = Field(alias="streamId")


class TenantEventsTenantEventsDataStreamIdle(BaseModel):
    """Payload of a PublicEvent: one member per public event type. Fields of the same
    name can have different types across members (reason is a different enum per
    event family); alias them when one selection covers several."""

    typename__: Literal["StreamIdle"] = Field(alias="__typename")
    stream_id: str = Field(alias="streamId")


class TenantEventsTenantEventsDataStreamKeyRotated(BaseModel):
    """Payload of a PublicEvent: one member per public event type. Fields of the same
    name can have different types across members (reason is a different enum per
    event family); alias them when one selection covers several."""

    typename__: Literal["StreamKeyRotated"] = Field(alias="__typename")
    stream_id: str = Field(alias="streamId")


class TenantEventsTenantEventsDataStreamLive(BaseModel):
    """Payload of a PublicEvent: one member per public event type. Fields of the same
    name can have different types across members (reason is a different enum per
    event family); alias them when one selection covers several."""

    typename__: Literal["StreamLive"] = Field(alias="__typename")
    stream_id: str = Field(alias="streamId")


class TenantEventsTenantEventsDataStreamUpdated(BaseModel):
    """Payload of a PublicEvent: one member per public event type. Fields of the same
    name can have different types across members (reason is a different enum per
    event family); alias them when one selection covers several."""

    typename__: Literal["StreamUpdated"] = Field(alias="__typename")
    stream_id: str = Field(alias="streamId")
    changed_fields: list[str] = Field(alias="changedFields")


class TenantEventsTenantEventsDataUploadAborted(BaseModel):
    """Payload of a PublicEvent: one member per public event type. Fields of the same
    name can have different types across members (reason is a different enum per
    event family); alias them when one selection covers several."""

    typename__: Literal["UploadAborted"] = Field(alias="__typename")
    artifact: Optional["TenantEventsTenantEventsDataUploadAbortedArtifact"]


class TenantEventsTenantEventsDataUploadAbortedArtifact(EventArtifact):
    """Part of a public event payload (frameworks.events.public.v1.Artifact)."""

    pass


class TenantEventsTenantEventsDataUploadCompleted(BaseModel):
    """Payload of a PublicEvent: one member per public event type. Fields of the same
    name can have different types across members (reason is a different enum per
    event family); alias them when one selection covers several."""

    typename__: Literal["UploadCompleted"] = Field(alias="__typename")
    artifact: Optional["TenantEventsTenantEventsDataUploadCompletedArtifact"]
    size_bytes: int = Field(alias="sizeBytes")


class TenantEventsTenantEventsDataUploadCompletedArtifact(EventArtifact):
    """Part of a public event payload (frameworks.events.public.v1.Artifact)."""

    pass


class TenantEventsTenantEventsDataUploadCreated(BaseModel):
    """Payload of a PublicEvent: one member per public event type. Fields of the same
    name can have different types across members (reason is a different enum per
    event family); alias them when one selection covers several."""

    typename__: Literal["UploadCreated"] = Field(alias="__typename")
    artifact: Optional["TenantEventsTenantEventsDataUploadCreatedArtifact"]
    filename: str
    expected_size_bytes: int = Field(alias="expectedSizeBytes")


class TenantEventsTenantEventsDataUploadCreatedArtifact(EventArtifact):
    """Part of a public event payload (frameworks.events.public.v1.Artifact)."""

    pass


class TenantEventsTenantEventsDataUploadFailed(BaseModel):
    """Payload of a PublicEvent: one member per public event type. Fields of the same
    name can have different types across members (reason is a different enum per
    event family); alias them when one selection covers several."""

    typename__: Literal["UploadFailed"] = Field(alias="__typename")
    artifact: Optional["TenantEventsTenantEventsDataUploadFailedArtifact"]
    media_failure_reason: EventMediaFailureReason = Field(alias="mediaFailureReason")


class TenantEventsTenantEventsDataUploadFailedArtifact(EventArtifact):
    """Part of a public event payload (frameworks.events.public.v1.Artifact)."""

    pass


class TenantEventsTenantEventsDataUploadReady(BaseModel):
    """Payload of a PublicEvent: one member per public event type. Fields of the same
    name can have different types across members (reason is a different enum per
    event family); alias them when one selection covers several."""

    typename__: Literal["UploadReady"] = Field(alias="__typename")
    artifact: Optional["TenantEventsTenantEventsDataUploadReadyArtifact"]
    duration_ms: int = Field(alias="durationMs")
    size_bytes: int = Field(alias="sizeBytes")


class TenantEventsTenantEventsDataUploadReadyArtifact(EventArtifact):
    """Part of a public event payload (frameworks.events.public.v1.Artifact)."""

    pass


TenantEvents.model_rebuild()
TenantEventsTenantEvents.model_rebuild()
TenantEventsTenantEventsDataInvoiceCreated.model_rebuild()
TenantEventsTenantEventsDataInvoicePaid.model_rebuild()
TenantEventsTenantEventsDataPaymentFailed.model_rebuild()
TenantEventsTenantEventsDataTopupCredited.model_rebuild()
TenantEventsTenantEventsDataClipFailed.model_rebuild()
TenantEventsTenantEventsDataClipReady.model_rebuild()
TenantEventsTenantEventsDataClipRequested.model_rebuild()
TenantEventsTenantEventsDataRecordingFailed.model_rebuild()
TenantEventsTenantEventsDataRecordingReady.model_rebuild()
TenantEventsTenantEventsDataUploadAborted.model_rebuild()
TenantEventsTenantEventsDataUploadCompleted.model_rebuild()
TenantEventsTenantEventsDataUploadCreated.model_rebuild()
TenantEventsTenantEventsDataUploadFailed.model_rebuild()
TenantEventsTenantEventsDataUploadReady.model_rebuild()
