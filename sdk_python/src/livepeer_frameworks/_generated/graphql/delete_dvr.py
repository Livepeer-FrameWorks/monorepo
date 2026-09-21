from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import AuthError, DeleteSuccess, NotFoundError


class DeleteDVR(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    delete_dvr: Annotated[
        Union[
            "DeleteDVRDeleteDvrDeleteSuccess",
            "DeleteDVRDeleteDvrNotFoundError",
            "DeleteDVRDeleteDvrAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(alias="deleteDVR", description="Delete a DVR recording.")
    "Delete a DVR recording."


class DeleteDVRDeleteDvrDeleteSuccess(DeleteSuccess):
    typename__: Literal["DeleteSuccess"] = Field(alias="__typename")


class DeleteDVRDeleteDvrNotFoundError(NotFoundError):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class DeleteDVRDeleteDvrAuthError(AuthError):
    typename__: Literal["AuthError"] = Field(alias="__typename")


DeleteDVR.model_rebuild()
