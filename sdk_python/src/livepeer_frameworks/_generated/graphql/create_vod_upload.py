from datetime import datetime
from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import AuthErrorFields, ValidationErrorFields


class CreateVodUpload(BaseModel):
    create_vod_upload: Annotated[
        Union[
            "CreateVodUploadCreateVodUploadVodUploadSession",
            "CreateVodUploadCreateVodUploadValidationError",
            "CreateVodUploadCreateVodUploadAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(alias="createVodUpload")


class CreateVodUploadCreateVodUploadVodUploadSession(BaseModel):
    typename__: Literal["VodUploadSession"] = Field(alias="__typename")
    id: str
    artifact_id: str = Field(alias="artifactId")
    artifact_hash: str = Field(alias="artifactHash")
    playback_id: str = Field(alias="playbackId")
    part_size: float = Field(alias="partSize")
    parts: list["CreateVodUploadCreateVodUploadVodUploadSessionParts"]
    expires_at: datetime = Field(alias="expiresAt")


class CreateVodUploadCreateVodUploadVodUploadSessionParts(BaseModel):
    part_number: int = Field(alias="partNumber")
    presigned_url: str = Field(alias="presignedUrl")


class CreateVodUploadCreateVodUploadValidationError(ValidationErrorFields):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class CreateVodUploadCreateVodUploadAuthError(AuthErrorFields):
    typename__: Literal["AuthError"] = Field(alias="__typename")


CreateVodUpload.model_rebuild()
CreateVodUploadCreateVodUploadVodUploadSession.model_rebuild()
