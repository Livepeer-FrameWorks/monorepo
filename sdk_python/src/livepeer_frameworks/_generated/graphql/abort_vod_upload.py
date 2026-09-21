from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import AuthErrorFields, DeleteSuccessFields, NotFoundErrorFields


class AbortVodUpload(BaseModel):
    abort_vod_upload: Annotated[
        Union[
            "AbortVodUploadAbortVodUploadDeleteSuccess",
            "AbortVodUploadAbortVodUploadNotFoundError",
            "AbortVodUploadAbortVodUploadAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(alias="abortVodUpload")


class AbortVodUploadAbortVodUploadDeleteSuccess(DeleteSuccessFields):
    typename__: Literal["DeleteSuccess"] = Field(alias="__typename")


class AbortVodUploadAbortVodUploadNotFoundError(NotFoundErrorFields):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class AbortVodUploadAbortVodUploadAuthError(AuthErrorFields):
    typename__: Literal["AuthError"] = Field(alias="__typename")


AbortVodUpload.model_rebuild()
