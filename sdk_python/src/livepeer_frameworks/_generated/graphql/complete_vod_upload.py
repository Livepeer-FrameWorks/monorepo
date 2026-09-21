from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (  # noqa: F401
    AuthError,
    NotFoundError,
    ValidationError,
    VodAsset,
    VodAssetEffectiveRetention,
    VodAssetPlaybackPolicy,
    VodAssetThumbnailAssets,
)


class CompleteVodUpload(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    complete_vod_upload: Annotated[
        Union[
            "CompleteVodUploadCompleteVodUploadVodAsset",
            "CompleteVodUploadCompleteVodUploadValidationError",
            "CompleteVodUploadCompleteVodUploadNotFoundError",
            "CompleteVodUploadCompleteVodUploadAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="completeVodUpload",
        description="Complete a VOD upload after all parts are uploaded.\nTriggers processing and thumbnail generation.",
    )
    "Complete a VOD upload after all parts are uploaded.\nTriggers processing and thumbnail generation."


class CompleteVodUploadCompleteVodUploadVodAsset(VodAsset):
    typename__: Literal["VodAsset"] = Field(alias="__typename")


class CompleteVodUploadCompleteVodUploadValidationError(ValidationError):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class CompleteVodUploadCompleteVodUploadNotFoundError(NotFoundError):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class CompleteVodUploadCompleteVodUploadAuthError(AuthError):
    typename__: Literal["AuthError"] = Field(alias="__typename")


CompleteVodUpload.model_rebuild()
