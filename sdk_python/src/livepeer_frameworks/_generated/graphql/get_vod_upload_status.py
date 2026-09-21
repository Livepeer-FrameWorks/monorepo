from datetime import datetime
from typing import Annotated, Literal, Optional, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .enums import VodAssetStatus
from .fragments import AuthError, NotFoundError, ValidationError


class GetVodUploadStatus(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    vod_upload_status: Annotated[
        Union[
            "GetVodUploadStatusVodUploadStatusVodUploadStatus",
            "GetVodUploadStatusVodUploadStatusValidationError",
            "GetVodUploadStatusVodUploadStatusNotFoundError",
            "GetVodUploadStatusVodUploadStatusAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="vodUploadStatus",
        description="Read server-authoritative state of an in-flight VOD upload session.\nPolling complement to the upload events of tenantEvents; intended for reload-recovery\nand agent workflows that need a request/response shape.",
    )
    "Read server-authoritative state of an in-flight VOD upload session.\nPolling complement to the upload events of tenantEvents; intended for reload-recovery\nand agent workflows that need a request/response shape."


class GetVodUploadStatusVodUploadStatusVodUploadStatus(BaseModel):
    typename__: Literal["VodUploadStatus"] = Field(alias="__typename")
    upload_id: str = Field(
        alias="uploadId",
        description="Opaque upload session ID returned by createVodUpload; preserve unchanged when resuming.",
    )
    "Opaque upload session ID returned by createVodUpload; preserve unchanged when resuming."
    state: VodAssetStatus = Field(description="Current state of the upload session.")
    "Current state of the upload session."
    expires_at: Optional[datetime] = Field(
        alias="expiresAt",
        description="S3 multipart session deadline. Past this point, state is EXPIRED.",
    )
    "S3 multipart session deadline. Past this point, state is EXPIRED."
    retention_until: Optional[datetime] = Field(
        alias="retentionUntil",
        description="Asset retention deadline (informational; distinct from upload-session expiry).",
    )
    "Asset retention deadline (informational; distinct from upload-session expiry)."
    uploaded_parts: list[
        "GetVodUploadStatusVodUploadStatusVodUploadStatusUploadedParts"
    ] = Field(alias="uploadedParts", description="Parts S3 has already received.")
    "Parts S3 has already received."
    missing_parts: list[int] = Field(
        alias="missingParts",
        description="Part numbers still missing for completion (1-indexed).",
    )
    "Part numbers still missing for completion (1-indexed)."
    last_error_code: Optional[str] = Field(
        alias="lastErrorCode",
        description="Last error code emitted by the pipeline, if any.",
    )
    "Last error code emitted by the pipeline, if any."
    artifact_hash: Optional[str] = Field(
        alias="artifactHash", description="Hash for playback URL resolution."
    )
    "Hash for playback URL resolution."
    playback_id: Optional[str] = Field(
        alias="playbackId",
        description="Public playback identifier (available once known).",
    )
    "Public playback identifier (available once known)."


class GetVodUploadStatusVodUploadStatusVodUploadStatusUploadedParts(BaseModel):
    """A part S3 has already received for an in-flight multipart upload (returned by ListParts).
    Used by the client uploader to reconcile local resume state against the server."""

    part_number: int = Field(alias="partNumber", description="1-indexed part number.")
    "1-indexed part number."
    etag: str = Field(description="ETag header value returned by S3.")
    "ETag header value returned by S3."
    size_bytes: float = Field(
        alias="sizeBytes", description="Size of the uploaded part in bytes."
    )
    "Size of the uploaded part in bytes."


class GetVodUploadStatusVodUploadStatusValidationError(ValidationError):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class GetVodUploadStatusVodUploadStatusNotFoundError(NotFoundError):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class GetVodUploadStatusVodUploadStatusAuthError(AuthError):
    typename__: Literal["AuthError"] = Field(alias="__typename")


GetVodUploadStatus.model_rebuild()
GetVodUploadStatusVodUploadStatusVodUploadStatus.model_rebuild()
