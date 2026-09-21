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
    tenant_events: "TenantEventsTenantEvents" = Field(alias="tenantEvents")


class TenantEventsTenantEvents(BaseModel):
    id: str
    type_: str = Field(alias="type")
    time: datetime
    subject: str
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
    typename__: Literal["AccountSuspended"] = Field(alias="__typename")
    suspension_reason: EventSuspensionReason = Field(alias="suspensionReason")


class TenantEventsTenantEventsDataApiTokenCreated(BaseModel):
    typename__: Literal["ApiTokenCreated"] = Field(alias="__typename")
    token_id: str = Field(alias="tokenId")
    name: str
    permissions: list[str]
    expires_at: Optional[datetime] = Field(alias="expiresAt")


class TenantEventsTenantEventsDataApiTokenRevoked(BaseModel):
    typename__: Literal["ApiTokenRevoked"] = Field(alias="__typename")
    token_id: str = Field(alias="tokenId")


class TenantEventsTenantEventsDataBillingDetailsUpdated(BaseModel):
    typename__: Literal["BillingDetailsUpdated"] = Field(alias="__typename")
    changed_fields: list[str] = Field(alias="changedFields")


class TenantEventsTenantEventsDataInvoiceCreated(BaseModel):
    typename__: Literal["InvoiceCreated"] = Field(alias="__typename")
    invoice_id: str = Field(alias="invoiceId")
    amount_due: Optional["TenantEventsTenantEventsDataInvoiceCreatedAmountDue"] = Field(
        alias="amountDue"
    )
    period_start: Optional[datetime] = Field(alias="periodStart")
    period_end: Optional[datetime] = Field(alias="periodEnd")
    due_at: Optional[datetime] = Field(alias="dueAt")


TenantEventsTenantEventsDataInvoiceCreatedAmountDue = EventMoney


class TenantEventsTenantEventsDataInvoicePaid(BaseModel):
    typename__: Literal["InvoicePaid"] = Field(alias="__typename")
    invoice_id: str = Field(alias="invoiceId")
    amount_paid: Optional["TenantEventsTenantEventsDataInvoicePaidAmountPaid"] = Field(
        alias="amountPaid"
    )


TenantEventsTenantEventsDataInvoicePaidAmountPaid = EventMoney


class TenantEventsTenantEventsDataPaymentFailed(BaseModel):
    typename__: Literal["PaymentFailed"] = Field(alias="__typename")
    payment_id: str = Field(alias="paymentId")
    invoice_id: str = Field(alias="invoiceId")
    amount: Optional["TenantEventsTenantEventsDataPaymentFailedAmount"]
    payment_failure_reason: EventPaymentFailureReason = Field(
        alias="paymentFailureReason"
    )
    provider: str
    provider_reference_id: str = Field(alias="providerReferenceId")


TenantEventsTenantEventsDataPaymentFailedAmount = EventMoney


class TenantEventsTenantEventsDataTopupCredited(BaseModel):
    typename__: Literal["TopupCredited"] = Field(alias="__typename")
    topup_id: str = Field(alias="topupId")
    amount: Optional["TenantEventsTenantEventsDataTopupCreditedAmount"]


TenantEventsTenantEventsDataTopupCreditedAmount = EventMoney


class TenantEventsTenantEventsDataClipFailed(BaseModel):
    typename__: Literal["ClipFailed"] = Field(alias="__typename")
    artifact: Optional["TenantEventsTenantEventsDataClipFailedArtifact"]
    media_failure_reason: EventMediaFailureReason = Field(alias="mediaFailureReason")


TenantEventsTenantEventsDataClipFailedArtifact = EventArtifact


class TenantEventsTenantEventsDataClipReady(BaseModel):
    typename__: Literal["ClipReady"] = Field(alias="__typename")
    artifact: Optional["TenantEventsTenantEventsDataClipReadyArtifact"]
    duration_ms: int = Field(alias="durationMs")
    size_bytes: int = Field(alias="sizeBytes")


TenantEventsTenantEventsDataClipReadyArtifact = EventArtifact


class TenantEventsTenantEventsDataClipRequested(BaseModel):
    typename__: Literal["ClipRequested"] = Field(alias="__typename")
    artifact: Optional["TenantEventsTenantEventsDataClipRequestedArtifact"]
    duration_ms: int = Field(alias="durationMs")


TenantEventsTenantEventsDataClipRequestedArtifact = EventArtifact


class TenantEventsTenantEventsDataCustomDomainFailed(BaseModel):
    typename__: Literal["CustomDomainFailed"] = Field(alias="__typename")
    domain: str
    custom_domain_failure_reason: EventCustomDomainFailureReason = Field(
        alias="customDomainFailureReason"
    )


class TenantEventsTenantEventsDataCustomDomainVerified(BaseModel):
    typename__: Literal["CustomDomainVerified"] = Field(alias="__typename")
    domain: str


class TenantEventsTenantEventsDataMultistreamStatusChanged(BaseModel):
    typename__: Literal["MultistreamStatusChanged"] = Field(alias="__typename")
    stream_id: str = Field(alias="streamId")
    target_id: str = Field(alias="targetId")
    target_name: str = Field(alias="targetName")
    status: EventMultistreamStatus
    previous_status: EventMultistreamStatus = Field(alias="previousStatus")


class TenantEventsTenantEventsDataRecordingFailed(BaseModel):
    typename__: Literal["RecordingFailed"] = Field(alias="__typename")
    artifact: Optional["TenantEventsTenantEventsDataRecordingFailedArtifact"]
    media_failure_reason: EventMediaFailureReason = Field(alias="mediaFailureReason")


TenantEventsTenantEventsDataRecordingFailedArtifact = EventArtifact


class TenantEventsTenantEventsDataRecordingReady(BaseModel):
    typename__: Literal["RecordingReady"] = Field(alias="__typename")
    artifact: Optional["TenantEventsTenantEventsDataRecordingReadyArtifact"]
    duration_ms: int = Field(alias="durationMs")
    size_bytes: int = Field(alias="sizeBytes")


TenantEventsTenantEventsDataRecordingReadyArtifact = EventArtifact


class TenantEventsTenantEventsDataStreamConnected(BaseModel):
    typename__: Literal["StreamConnected"] = Field(alias="__typename")
    stream_id: str = Field(alias="streamId")
    protocol: EventIngestProtocol


class TenantEventsTenantEventsDataStreamCreated(BaseModel):
    typename__: Literal["StreamCreated"] = Field(alias="__typename")
    stream_id: str = Field(alias="streamId")
    name: str
    playback_id: str = Field(alias="playbackId")


class TenantEventsTenantEventsDataStreamDeleted(BaseModel):
    typename__: Literal["StreamDeleted"] = Field(alias="__typename")
    stream_id: str = Field(alias="streamId")


class TenantEventsTenantEventsDataStreamIdle(BaseModel):
    typename__: Literal["StreamIdle"] = Field(alias="__typename")
    stream_id: str = Field(alias="streamId")


class TenantEventsTenantEventsDataStreamKeyRotated(BaseModel):
    typename__: Literal["StreamKeyRotated"] = Field(alias="__typename")
    stream_id: str = Field(alias="streamId")


class TenantEventsTenantEventsDataStreamLive(BaseModel):
    typename__: Literal["StreamLive"] = Field(alias="__typename")
    stream_id: str = Field(alias="streamId")


class TenantEventsTenantEventsDataStreamUpdated(BaseModel):
    typename__: Literal["StreamUpdated"] = Field(alias="__typename")
    stream_id: str = Field(alias="streamId")
    changed_fields: list[str] = Field(alias="changedFields")


class TenantEventsTenantEventsDataUploadAborted(BaseModel):
    typename__: Literal["UploadAborted"] = Field(alias="__typename")
    artifact: Optional["TenantEventsTenantEventsDataUploadAbortedArtifact"]


TenantEventsTenantEventsDataUploadAbortedArtifact = EventArtifact


class TenantEventsTenantEventsDataUploadCompleted(BaseModel):
    typename__: Literal["UploadCompleted"] = Field(alias="__typename")
    artifact: Optional["TenantEventsTenantEventsDataUploadCompletedArtifact"]
    size_bytes: int = Field(alias="sizeBytes")


TenantEventsTenantEventsDataUploadCompletedArtifact = EventArtifact


class TenantEventsTenantEventsDataUploadCreated(BaseModel):
    typename__: Literal["UploadCreated"] = Field(alias="__typename")
    artifact: Optional["TenantEventsTenantEventsDataUploadCreatedArtifact"]
    filename: str
    expected_size_bytes: int = Field(alias="expectedSizeBytes")


TenantEventsTenantEventsDataUploadCreatedArtifact = EventArtifact


class TenantEventsTenantEventsDataUploadFailed(BaseModel):
    typename__: Literal["UploadFailed"] = Field(alias="__typename")
    artifact: Optional["TenantEventsTenantEventsDataUploadFailedArtifact"]
    media_failure_reason: EventMediaFailureReason = Field(alias="mediaFailureReason")


TenantEventsTenantEventsDataUploadFailedArtifact = EventArtifact


class TenantEventsTenantEventsDataUploadReady(BaseModel):
    typename__: Literal["UploadReady"] = Field(alias="__typename")
    artifact: Optional["TenantEventsTenantEventsDataUploadReadyArtifact"]
    duration_ms: int = Field(alias="durationMs")
    size_bytes: int = Field(alias="sizeBytes")


TenantEventsTenantEventsDataUploadReadyArtifact = EventArtifact
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
