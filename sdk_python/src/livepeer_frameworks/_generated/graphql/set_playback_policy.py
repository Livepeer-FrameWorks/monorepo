from typing import Annotated, Literal, Optional, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (  # noqa: F401
    AuthError,
    NotFoundError,
    PlaybackPolicy,
    PlaybackPolicyJwt,
    PlaybackPolicyWebhook,
    ValidationError,
)


class SetPlaybackPolicy(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

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
    ] = Field(
        alias="setPlaybackPolicy",
        description="Set or clear the playback access policy on a stream, VOD asset, or clip.\nExactly one of streamId / vodAssetId / clipId must be set in the input.\nWebhook secrets are write-only on input and never returned in queries.\nMutating a policy invalidates Foghorn caches and re-runs USER_NEW for\naffected sessions; valid viewers continue, invalid ones are denied.",
    )
    "Set or clear the playback access policy on a stream, VOD asset, or clip.\nExactly one of streamId / vodAssetId / clipId must be set in the input.\nWebhook secrets are write-only on input and never returned in queries.\nMutating a policy invalidates Foghorn caches and re-runs USER_NEW for\naffected sessions; valid viewers continue, invalid ones are denied."


class SetPlaybackPolicySetPlaybackPolicyStream(BaseModel):
    typename__: Literal["Stream"] = Field(alias="__typename")
    id: str = Field(description="Global unique identifier for Relay compatibility.")
    "Global unique identifier for Relay compatibility."
    playback_policy: Optional[
        "SetPlaybackPolicySetPlaybackPolicyStreamPlaybackPolicy"
    ] = Field(
        alias="playbackPolicy",
        description="Playback access policy. null/PUBLIC = anyone with the playbackId can watch.",
    )
    "Playback access policy. null/PUBLIC = anyone with the playbackId can watch."


class SetPlaybackPolicySetPlaybackPolicyStreamPlaybackPolicy(PlaybackPolicy):
    """Per-playback-object access policy. Foghorn reads this in the USER_NEW
    trigger handler; the requiresAuth marker (on the playback object itself)
    gates whether the full policy is fetched at all."""

    pass


class SetPlaybackPolicySetPlaybackPolicyVodAsset(BaseModel):
    typename__: Literal["VodAsset"] = Field(alias="__typename")
    id: str = Field(description="Global unique identifier for Relay compatibility.")
    "Global unique identifier for Relay compatibility."
    playback_policy: Optional[
        "SetPlaybackPolicySetPlaybackPolicyVodAssetPlaybackPolicy"
    ] = Field(
        alias="playbackPolicy",
        description="Playback access policy. null/PUBLIC = anyone with the playbackId can watch.",
    )
    "Playback access policy. null/PUBLIC = anyone with the playbackId can watch."


class SetPlaybackPolicySetPlaybackPolicyVodAssetPlaybackPolicy(PlaybackPolicy):
    """Per-playback-object access policy. Foghorn reads this in the USER_NEW
    trigger handler; the requiresAuth marker (on the playback object itself)
    gates whether the full policy is fetched at all."""

    pass


class SetPlaybackPolicySetPlaybackPolicyClip(BaseModel):
    typename__: Literal["Clip"] = Field(alias="__typename")
    id: str = Field(description="Global unique identifier for Relay compatibility.")
    "Global unique identifier for Relay compatibility."
    playback_policy: Optional[
        "SetPlaybackPolicySetPlaybackPolicyClipPlaybackPolicy"
    ] = Field(
        alias="playbackPolicy",
        description="Playback access policy snapshotted at clip creation. null/PUBLIC means\nanyone with the playbackId can watch. Independent from the source stream's\npolicy after creation — flipping the source stream's policy does not affect\nalready-shared clip URLs.",
    )
    "Playback access policy snapshotted at clip creation. null/PUBLIC means\nanyone with the playbackId can watch. Independent from the source stream's\npolicy after creation — flipping the source stream's policy does not affect\nalready-shared clip URLs."


class SetPlaybackPolicySetPlaybackPolicyClipPlaybackPolicy(PlaybackPolicy):
    """Per-playback-object access policy. Foghorn reads this in the USER_NEW
    trigger handler; the requiresAuth marker (on the playback object itself)
    gates whether the full policy is fetched at all."""

    pass


class SetPlaybackPolicySetPlaybackPolicyValidationError(ValidationError):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class SetPlaybackPolicySetPlaybackPolicyNotFoundError(NotFoundError):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class SetPlaybackPolicySetPlaybackPolicyAuthError(AuthError):
    typename__: Literal["AuthError"] = Field(alias="__typename")


SetPlaybackPolicy.model_rebuild()
SetPlaybackPolicySetPlaybackPolicyStream.model_rebuild()
SetPlaybackPolicySetPlaybackPolicyVodAsset.model_rebuild()
SetPlaybackPolicySetPlaybackPolicyClip.model_rebuild()
