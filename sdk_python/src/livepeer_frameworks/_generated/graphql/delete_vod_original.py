from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (
    AuthErrorDefault,
    DeleteSuccessDefault,
    NotFoundErrorDefault,
    ValidationErrorDefault,
)


class DeleteVodOriginal(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    delete_vod_original: Annotated[
        Union[
            "DeleteVodOriginalDeleteVodOriginalDeleteSuccess",
            "DeleteVodOriginalDeleteVodOriginalValidationError",
            "DeleteVodOriginalDeleteVodOriginalNotFoundError",
            "DeleteVodOriginalDeleteVodOriginalAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="deleteVodOriginal",
        description="Permanently delete only the retained upload; processed playback remains available.",
    )
    "Permanently delete only the retained upload; processed playback remains available."


class DeleteVodOriginalDeleteVodOriginalDeleteSuccess(DeleteSuccessDefault):
    typename__: Literal["DeleteSuccess"] = Field(alias="__typename")


class DeleteVodOriginalDeleteVodOriginalValidationError(ValidationErrorDefault):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class DeleteVodOriginalDeleteVodOriginalNotFoundError(NotFoundErrorDefault):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class DeleteVodOriginalDeleteVodOriginalAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


DeleteVodOriginal.model_rebuild()
