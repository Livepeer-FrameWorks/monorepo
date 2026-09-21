from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import AuthErrorFields, DeleteSuccessFields, NotFoundErrorFields


class DeleteVodAsset(BaseModel):
    delete_vod_asset: Annotated[
        Union[
            "DeleteVodAssetDeleteVodAssetDeleteSuccess",
            "DeleteVodAssetDeleteVodAssetNotFoundError",
            "DeleteVodAssetDeleteVodAssetAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(alias="deleteVodAsset")


class DeleteVodAssetDeleteVodAssetDeleteSuccess(DeleteSuccessFields):
    typename__: Literal["DeleteSuccess"] = Field(alias="__typename")


class DeleteVodAssetDeleteVodAssetNotFoundError(NotFoundErrorFields):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class DeleteVodAssetDeleteVodAssetAuthError(AuthErrorFields):
    typename__: Literal["AuthError"] = Field(alias="__typename")


DeleteVodAsset.model_rebuild()
