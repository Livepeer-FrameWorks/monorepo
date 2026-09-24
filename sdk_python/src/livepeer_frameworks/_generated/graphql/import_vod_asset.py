from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (  # noqa: F401
    AuthError,
    ValidationError,
    VodAsset,
    VodAssetEffectiveRetention,
    VodAssetPlaybackPolicy,
    VodAssetThumbnailAssets,
)


class ImportVodAsset(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    import_vod_asset: Annotated[
        Union[
            "ImportVodAssetImportVodAssetVodAsset",
            "ImportVodAssetImportVodAssetValidationError",
            "ImportVodAssetImportVodAssetAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="importVodAsset",
        description="Import a video from a public https or http URL as a VOD asset. The processing\nnode reads the file from the URL and processes it like an upload; the asset\nreports PROCESSING until it is ready. Progress arrives as upload.created,\nupload.completed, and upload.ready or upload.failed events.",
    )
    "Import a video from a public https or http URL as a VOD asset. The processing\nnode reads the file from the URL and processes it like an upload; the asset\nreports PROCESSING until it is ready. Progress arrives as upload.created,\nupload.completed, and upload.ready or upload.failed events."


class ImportVodAssetImportVodAssetVodAsset(VodAsset):
    typename__: Literal["VodAsset"] = Field(alias="__typename")


class ImportVodAssetImportVodAssetValidationError(ValidationError):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class ImportVodAssetImportVodAssetAuthError(AuthError):
    typename__: Literal["AuthError"] = Field(alias="__typename")


ImportVodAsset.model_rebuild()
