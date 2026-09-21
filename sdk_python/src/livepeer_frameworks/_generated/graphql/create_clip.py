from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (  # noqa: F401
    AuthErrorFields,
    ClipFields,
    ClipFieldsEffectiveRetention,
    ClipFieldsPlaybackPolicy,
    ClipFieldsThumbnailAssets,
    NotFoundErrorFields,
    ValidationErrorFields,
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


class CreateClipCreateClipClip(ClipFields):
    typename__: Literal["Clip"] = Field(alias="__typename")


class CreateClipCreateClipValidationError(ValidationErrorFields):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class CreateClipCreateClipNotFoundError(NotFoundErrorFields):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class CreateClipCreateClipAuthError(AuthErrorFields):
    typename__: Literal["AuthError"] = Field(alias="__typename")


CreateClip.model_rebuild()
