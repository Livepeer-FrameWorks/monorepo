from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (  # noqa: F401
    AuthErrorFields,
    NotFoundErrorFields,
    ValidationErrorFields,
    VodAssetFields,
    VodAssetFieldsEffectiveRetention,
    VodAssetFieldsPlaybackPolicy,
    VodAssetFieldsThumbnailAssets,
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


class CompleteVodUploadCompleteVodUploadVodAsset(VodAssetFields):
    typename__: Literal["VodAsset"] = Field(alias="__typename")


class CompleteVodUploadCompleteVodUploadValidationError(ValidationErrorFields):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class CompleteVodUploadCompleteVodUploadNotFoundError(NotFoundErrorFields):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class CompleteVodUploadCompleteVodUploadAuthError(AuthErrorFields):
    typename__: Literal["AuthError"] = Field(alias="__typename")


CompleteVodUpload.model_rebuild()
