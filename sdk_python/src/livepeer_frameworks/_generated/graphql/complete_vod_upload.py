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
    complete_vod_upload: Annotated[
        Union[
            "CompleteVodUploadCompleteVodUploadVodAsset",
            "CompleteVodUploadCompleteVodUploadValidationError",
            "CompleteVodUploadCompleteVodUploadNotFoundError",
            "CompleteVodUploadCompleteVodUploadAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(alias="completeVodUpload")


class CompleteVodUploadCompleteVodUploadVodAsset(VodAsset):
    typename__: Literal["VodAsset"] = Field(alias="__typename")


class CompleteVodUploadCompleteVodUploadValidationError(ValidationError):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class CompleteVodUploadCompleteVodUploadNotFoundError(NotFoundError):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class CompleteVodUploadCompleteVodUploadAuthError(AuthError):
    typename__: Literal["AuthError"] = Field(alias="__typename")


CompleteVodUpload.model_rebuild()
