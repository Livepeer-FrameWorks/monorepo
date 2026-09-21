from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import AuthError, DeleteSuccess, NotFoundError


class AbortVodUpload(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    abort_vod_upload: Annotated[
        Union[
            "AbortVodUploadAbortVodUploadDeleteSuccess",
            "AbortVodUploadAbortVodUploadNotFoundError",
            "AbortVodUploadAbortVodUploadAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(alias="abortVodUpload", description="Abort an in-progress VOD upload.")
    "Abort an in-progress VOD upload."


class AbortVodUploadAbortVodUploadDeleteSuccess(DeleteSuccess):
    typename__: Literal["DeleteSuccess"] = Field(alias="__typename")


class AbortVodUploadAbortVodUploadNotFoundError(NotFoundError):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class AbortVodUploadAbortVodUploadAuthError(AuthError):
    typename__: Literal["AuthError"] = Field(alias="__typename")


AbortVodUpload.model_rebuild()
