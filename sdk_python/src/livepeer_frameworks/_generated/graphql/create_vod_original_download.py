from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (
    AuthErrorDefault,
    NotFoundErrorDefault,
    ValidationErrorDefault,
    VodOriginalDownloadDefault,
)


class CreateVodOriginalDownload(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    create_vod_original_download: Annotated[
        Union[
            "CreateVodOriginalDownloadCreateVodOriginalDownloadVodOriginalDownload",
            "CreateVodOriginalDownloadCreateVodOriginalDownloadValidationError",
            "CreateVodOriginalDownloadCreateVodOriginalDownloadNotFoundError",
            "CreateVodOriginalDownloadCreateVodOriginalDownloadAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="createVodOriginalDownload",
        description="Issue a free, owner-authenticated original download URL valid for 15 minutes. Limited to 20 per minute, burst 5.",
    )
    "Issue a free, owner-authenticated original download URL valid for 15 minutes. Limited to 20 per minute, burst 5."


class CreateVodOriginalDownloadCreateVodOriginalDownloadVodOriginalDownload(
    VodOriginalDownloadDefault
):
    typename__: Literal["VodOriginalDownload"] = Field(alias="__typename")


class CreateVodOriginalDownloadCreateVodOriginalDownloadValidationError(
    ValidationErrorDefault
):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class CreateVodOriginalDownloadCreateVodOriginalDownloadNotFoundError(
    NotFoundErrorDefault
):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class CreateVodOriginalDownloadCreateVodOriginalDownloadAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


CreateVodOriginalDownload.model_rebuild()
