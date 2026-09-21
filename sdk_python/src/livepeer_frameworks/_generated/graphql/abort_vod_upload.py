from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import AuthError, DeleteSuccess, NotFoundError


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


class AbortVodUploadAbortVodUploadDeleteSuccess(DeleteSuccess):
    typename__: Literal["DeleteSuccess"] = Field(alias="__typename")


class AbortVodUploadAbortVodUploadNotFoundError(NotFoundError):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class AbortVodUploadAbortVodUploadAuthError(AuthError):
    typename__: Literal["AuthError"] = Field(alias="__typename")


AbortVodUpload.model_rebuild()
