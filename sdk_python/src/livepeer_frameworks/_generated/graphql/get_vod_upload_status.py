from datetime import datetime
from typing import Annotated, Literal, Optional, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .enums import VodAssetStatus
from .fragments import AuthErrorFields, NotFoundErrorFields, ValidationErrorFields


class GetVodUploadStatus(BaseModel):
    vod_upload_status: Annotated[
        Union[
            "GetVodUploadStatusVodUploadStatusVodUploadStatus",
            "GetVodUploadStatusVodUploadStatusValidationError",
            "GetVodUploadStatusVodUploadStatusNotFoundError",
            "GetVodUploadStatusVodUploadStatusAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(alias="vodUploadStatus")


class GetVodUploadStatusVodUploadStatusVodUploadStatus(BaseModel):
    typename__: Literal["VodUploadStatus"] = Field(alias="__typename")
    upload_id: str = Field(alias="uploadId")
    state: VodAssetStatus
    expires_at: Optional[datetime] = Field(alias="expiresAt")
    retention_until: Optional[datetime] = Field(alias="retentionUntil")
    uploaded_parts: list[
        "GetVodUploadStatusVodUploadStatusVodUploadStatusUploadedParts"
    ] = Field(alias="uploadedParts")
    missing_parts: list[int] = Field(alias="missingParts")
    last_error_code: Optional[str] = Field(alias="lastErrorCode")
    artifact_hash: Optional[str] = Field(alias="artifactHash")
    playback_id: Optional[str] = Field(alias="playbackId")


class GetVodUploadStatusVodUploadStatusVodUploadStatusUploadedParts(BaseModel):
    part_number: int = Field(alias="partNumber")
    etag: str
    size_bytes: float = Field(alias="sizeBytes")


class GetVodUploadStatusVodUploadStatusValidationError(ValidationErrorFields):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class GetVodUploadStatusVodUploadStatusNotFoundError(NotFoundErrorFields):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class GetVodUploadStatusVodUploadStatusAuthError(AuthErrorFields):
    typename__: Literal["AuthError"] = Field(alias="__typename")


GetVodUploadStatus.model_rebuild()
GetVodUploadStatusVodUploadStatusVodUploadStatus.model_rebuild()
