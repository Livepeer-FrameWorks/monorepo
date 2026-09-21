from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (  # noqa: F401
    AuthError,
    Stream,
    StreamMetrics,
    StreamPlaybackPolicy,
    StreamPullSource,
    ValidationError,
)


class CreateStream(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    create_stream: Annotated[
        Union[
            "CreateStreamCreateStreamStream",
            "CreateStreamCreateStreamValidationError",
            "CreateStreamCreateStreamAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="createStream", description="Create a new stream for live broadcasting."
    )
    "Create a new stream for live broadcasting."


class CreateStreamCreateStreamStream(Stream):
    typename__: Literal["Stream"] = Field(alias="__typename")


class CreateStreamCreateStreamValidationError(ValidationError):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class CreateStreamCreateStreamAuthError(AuthError):
    typename__: Literal["AuthError"] = Field(alias="__typename")


CreateStream.model_rebuild()
