from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import AuthError, DeleteSuccess, NotFoundError


class DeleteVodAsset(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    delete_vod_asset: Annotated[
        Union[
            "DeleteVodAssetDeleteVodAssetDeleteSuccess",
            "DeleteVodAssetDeleteVodAssetNotFoundError",
            "DeleteVodAssetDeleteVodAssetAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(alias="deleteVodAsset", description="Delete a VOD asset.")
    "Delete a VOD asset."


class DeleteVodAssetDeleteVodAssetDeleteSuccess(DeleteSuccess):
    typename__: Literal["DeleteSuccess"] = Field(alias="__typename")


class DeleteVodAssetDeleteVodAssetNotFoundError(NotFoundError):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class DeleteVodAssetDeleteVodAssetAuthError(AuthError):
    typename__: Literal["AuthError"] = Field(alias="__typename")


DeleteVodAsset.model_rebuild()
