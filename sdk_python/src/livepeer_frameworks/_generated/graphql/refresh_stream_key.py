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


class RefreshStreamKey(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    refresh_stream_key: Annotated[
        Union[
            "RefreshStreamKeyRefreshStreamKeyStream",
            "RefreshStreamKeyRefreshStreamKeyValidationError",
            "RefreshStreamKeyRefreshStreamKeyNotFoundError",
            "RefreshStreamKeyRefreshStreamKeyAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="refreshStreamKey",
        description="Generate a new stream key, invalidating the old one.",
    )
    "Generate a new stream key, invalidating the old one."


class RefreshStreamKeyRefreshStreamKeyStream(Stream):
    typename__: Literal["Stream"] = Field(alias="__typename")


class RefreshStreamKeyRefreshStreamKeyValidationError(ValidationError):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class RefreshStreamKeyRefreshStreamKeyNotFoundError(NotFoundError):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class RefreshStreamKeyRefreshStreamKeyAuthError(AuthError):
    typename__: Literal["AuthError"] = Field(alias="__typename")


RefreshStreamKey.model_rebuild()
