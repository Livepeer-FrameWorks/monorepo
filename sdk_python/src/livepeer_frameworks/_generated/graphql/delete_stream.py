from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import AuthError, DeleteSuccess, NotFoundError


class DeleteStream(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    delete_stream: Annotated[
        Union[
            "DeleteStreamDeleteStreamDeleteSuccess",
            "DeleteStreamDeleteStreamNotFoundError",
            "DeleteStreamDeleteStreamAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="deleteStream", description="Delete a stream and all associated data."
    )
    "Delete a stream and all associated data."


class DeleteStreamDeleteStreamDeleteSuccess(DeleteSuccess):
    typename__: Literal["DeleteSuccess"] = Field(alias="__typename")


class DeleteStreamDeleteStreamNotFoundError(NotFoundError):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class DeleteStreamDeleteStreamAuthError(AuthError):
    typename__: Literal["AuthError"] = Field(alias="__typename")


DeleteStream.model_rebuild()
