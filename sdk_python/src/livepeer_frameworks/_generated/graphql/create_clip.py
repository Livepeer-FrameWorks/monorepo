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
    create_clip: Annotated[
        Union[
            "CreateClipCreateClipClip",
            "CreateClipCreateClipValidationError",
            "CreateClipCreateClipNotFoundError",
            "CreateClipCreateClipAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(alias="createClip")


class CreateClipCreateClipClip(Clip):
    typename__: Literal["Clip"] = Field(alias="__typename")


class CreateClipCreateClipValidationError(ValidationError):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class CreateClipCreateClipNotFoundError(NotFoundError):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class CreateClipCreateClipAuthError(AuthError):
    typename__: Literal["AuthError"] = Field(alias="__typename")


CreateClip.model_rebuild()
