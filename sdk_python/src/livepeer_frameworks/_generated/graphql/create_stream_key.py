from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import AuthError, NotFoundError, StreamKey, ValidationError


class CreateStreamKey(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    create_stream_key: Annotated[
        Union[
            "CreateStreamKeyCreateStreamKeyStreamKey",
            "CreateStreamKeyCreateStreamKeyValidationError",
            "CreateStreamKeyCreateStreamKeyNotFoundError",
            "CreateStreamKeyCreateStreamKeyAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="createStreamKey",
        description="Create an additional stream key for a stream.",
    )
    "Create an additional stream key for a stream."


class CreateStreamKeyCreateStreamKeyStreamKey(StreamKey):
    typename__: Literal["StreamKey"] = Field(alias="__typename")


class CreateStreamKeyCreateStreamKeyValidationError(ValidationError):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class CreateStreamKeyCreateStreamKeyNotFoundError(NotFoundError):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class CreateStreamKeyCreateStreamKeyAuthError(AuthError):
    typename__: Literal["AuthError"] = Field(alias="__typename")


CreateStreamKey.model_rebuild()
