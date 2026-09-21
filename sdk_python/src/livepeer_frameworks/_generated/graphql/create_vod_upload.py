from datetime import datetime
from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import AuthError, ValidationError


class CreateVodUpload(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    create_vod_upload: Annotated[
        Union[
            "CreateVodUploadCreateVodUploadVodUploadSession",
            "CreateVodUploadCreateVodUploadValidationError",
            "CreateVodUploadCreateVodUploadAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="createVodUpload",
        description="Create a new VOD upload session.\nReturns presigned URLs for multipart upload.",
    )
    "Create a new VOD upload session.\nReturns presigned URLs for multipart upload."


class CreateVodUploadCreateVodUploadVodUploadSession(BaseModel):
    typename__: Literal["VodUploadSession"] = Field(alias="__typename")
    id: str = Field(description="Upload session ID (S3 uploadId).")
    "Upload session ID (S3 uploadId)."
    artifact_id: str = Field(alias="artifactId", description="Internal artifact ID.")
    "Internal artifact ID."
    artifact_hash: str = Field(
        alias="artifactHash", description="Hash for playback URL resolution."
    )
    "Hash for playback URL resolution."
    playback_id: str = Field(
        alias="playbackId", description="Public playback identifier."
    )
    "Public playback identifier."
    part_size: float = Field(
        alias="partSize", description="Recommended part size in bytes."
    )
    "Recommended part size in bytes."
    parts: list["CreateVodUploadCreateVodUploadVodUploadSessionParts"] = Field(
        description="Presigned URLs for each part."
    )
    "Presigned URLs for each part."
    expires_at: datetime = Field(
        alias="expiresAt", description="When presigned URLs expire (typically 2 hours)."
    )
    "When presigned URLs expire (typically 2 hours)."


class CreateVodUploadCreateVodUploadVodUploadSessionParts(BaseModel):
    """Individual part upload instruction with presigned S3 URL."""

    part_number: int = Field(alias="partNumber", description="1-indexed part number.")
    "1-indexed part number."
    presigned_url: str = Field(
        alias="presignedUrl", description="Presigned PUT URL for uploading this part."
    )
    "Presigned PUT URL for uploading this part."


class CreateVodUploadCreateVodUploadValidationError(ValidationError):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class CreateVodUploadCreateVodUploadAuthError(AuthError):
    typename__: Literal["AuthError"] = Field(alias="__typename")


CreateVodUpload.model_rebuild()
CreateVodUploadCreateVodUploadVodUploadSession.model_rebuild()
