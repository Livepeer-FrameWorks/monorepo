from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import AuthError, DeleteSuccess, NotFoundError


class DeleteStreamKey(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    delete_stream_key: Annotated[
        Union[
            "DeleteStreamKeyDeleteStreamKeyDeleteSuccess",
            "DeleteStreamKeyDeleteStreamKeyNotFoundError",
            "DeleteStreamKeyDeleteStreamKeyAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(alias="deleteStreamKey", description="Delete a stream key.")
    "Delete a stream key."


class DeleteStreamKeyDeleteStreamKeyDeleteSuccess(DeleteSuccess):
    typename__: Literal["DeleteSuccess"] = Field(alias="__typename")


class DeleteStreamKeyDeleteStreamKeyNotFoundError(NotFoundError):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class DeleteStreamKeyDeleteStreamKeyAuthError(AuthError):
    typename__: Literal["AuthError"] = Field(alias="__typename")


DeleteStreamKey.model_rebuild()
