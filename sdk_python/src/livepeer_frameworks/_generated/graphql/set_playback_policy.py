from typing import Annotated, Literal, Optional, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (
    AuthErrorFields,
    NotFoundErrorFields,
    PlaybackPolicyFields,
    ValidationErrorFields,
)


class SetPlaybackPolicy(BaseModel):
    set_playback_policy: Annotated[
        Union[
            "SetPlaybackPolicySetPlaybackPolicyStream",
            "SetPlaybackPolicySetPlaybackPolicyVodAsset",
            "SetPlaybackPolicySetPlaybackPolicyClip",
            "SetPlaybackPolicySetPlaybackPolicyValidationError",
            "SetPlaybackPolicySetPlaybackPolicyNotFoundError",
            "SetPlaybackPolicySetPlaybackPolicyAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(alias="setPlaybackPolicy")


class SetPlaybackPolicySetPlaybackPolicyStream(BaseModel):
    typename__: Literal["Stream"] = Field(alias="__typename")
    id: str
    playback_policy: Optional[
        "SetPlaybackPolicySetPlaybackPolicyStreamPlaybackPolicy"
    ] = Field(alias="playbackPolicy")


SetPlaybackPolicySetPlaybackPolicyStreamPlaybackPolicy = PlaybackPolicyFields


class SetPlaybackPolicySetPlaybackPolicyVodAsset(BaseModel):
    typename__: Literal["VodAsset"] = Field(alias="__typename")
    id: str
    playback_policy: Optional[
        "SetPlaybackPolicySetPlaybackPolicyVodAssetPlaybackPolicy"
    ] = Field(alias="playbackPolicy")


SetPlaybackPolicySetPlaybackPolicyVodAssetPlaybackPolicy = PlaybackPolicyFields


class SetPlaybackPolicySetPlaybackPolicyClip(BaseModel):
    typename__: Literal["Clip"] = Field(alias="__typename")
    id: str
    playback_policy: Optional[
        "SetPlaybackPolicySetPlaybackPolicyClipPlaybackPolicy"
    ] = Field(alias="playbackPolicy")


SetPlaybackPolicySetPlaybackPolicyClipPlaybackPolicy = PlaybackPolicyFields


class SetPlaybackPolicySetPlaybackPolicyValidationError(ValidationErrorFields):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class SetPlaybackPolicySetPlaybackPolicyNotFoundError(NotFoundErrorFields):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class SetPlaybackPolicySetPlaybackPolicyAuthError(AuthErrorFields):
    typename__: Literal["AuthError"] = Field(alias="__typename")


SetPlaybackPolicy.model_rebuild()
SetPlaybackPolicySetPlaybackPolicyStream.model_rebuild()
SetPlaybackPolicySetPlaybackPolicyVodAsset.model_rebuild()
SetPlaybackPolicySetPlaybackPolicyClip.model_rebuild()
