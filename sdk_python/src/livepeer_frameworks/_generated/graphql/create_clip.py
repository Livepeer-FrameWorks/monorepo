from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (  # noqa: F401
    AuthError,
    Clip,
    ClipEffectiveRetention,
    ClipPlaybackPolicy,
    ClipThumbnailAssets,
    NotFoundError,
    ValidationError,
)


class CreateClip(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    create_clip: Annotated[
        Union[
            "CreateClipCreateClipClip",
            "CreateClipCreateClipValidationError",
            "CreateClipCreateClipNotFoundError",
            "CreateClipCreateClipAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="createClip",
        description="Create a clip from a live or recorded stream.\nClips are short video segments extracted from a stream.",
    )
    "Create a clip from a live or recorded stream.\nClips are short video segments extracted from a stream."


class CreateClipCreateClipClip(Clip):
    typename__: Literal["Clip"] = Field(alias="__typename")


class CreateClipCreateClipValidationError(ValidationError):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class CreateClipCreateClipNotFoundError(NotFoundError):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class CreateClipCreateClipAuthError(AuthError):
    typename__: Literal["AuthError"] = Field(alias="__typename")


CreateClip.model_rebuild()
