from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (  # noqa: F401
    AuthError,
    NotFoundError,
    Stream,
    StreamMetrics,
    StreamPlaybackPolicy,
    StreamPullSource,
    ValidationError,
)


class UpdateStream(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    update_stream: Annotated[
        Union[
            "UpdateStreamUpdateStreamStream",
            "UpdateStreamUpdateStreamValidationError",
            "UpdateStreamUpdateStreamNotFoundError",
            "UpdateStreamUpdateStreamAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="updateStream", description="Update an existing stream's configuration."
    )
    "Update an existing stream's configuration."


class UpdateStreamUpdateStreamStream(Stream):
    typename__: Literal["Stream"] = Field(alias="__typename")


class UpdateStreamUpdateStreamValidationError(ValidationError):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class UpdateStreamUpdateStreamNotFoundError(NotFoundError):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class UpdateStreamUpdateStreamAuthError(AuthError):
    typename__: Literal["AuthError"] = Field(alias="__typename")


UpdateStream.model_rebuild()
