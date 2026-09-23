from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (
    AuthErrorDefault,
    MediaRetentionPolicyDefault,
    MediaRetentionPolicyDefaultBounds,  # noqa: F401
    ValidationErrorDefault,
)


class SetMediaRetentionPolicy(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    set_media_retention_policy: Annotated[
        Union[
            "SetMediaRetentionPolicySetMediaRetentionPolicyMediaRetentionPolicy",
            "SetMediaRetentionPolicySetMediaRetentionPolicyValidationError",
            "SetMediaRetentionPolicySetMediaRetentionPolicyAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="setMediaRetentionPolicy",
        description='Set the tenant per-class retention default. `targetType` picks the asset\nclass (VOD, DVR, or CLIP). `days` is in [0, tier cap] where 0 means "keep\nforever" — only honored on uncapped (paid) tiers; Free clamps to its cap\nat write time. `clear: true` NULLs the column so the tenant inherits the\nper-class system default (VOD: keep forever; DVR/clip: 30d). The tier\ncap is exposed via `mediaRetentionPolicy.bounds.maxRecordingRetentionDays`.',
    )
    'Set the tenant per-class retention default. `targetType` picks the asset\nclass (VOD, DVR, or CLIP). `days` is in [0, tier cap] where 0 means "keep\nforever" — only honored on uncapped (paid) tiers; Free clamps to its cap\nat write time. `clear: true` NULLs the column so the tenant inherits the\nper-class system default (VOD: keep forever; DVR/clip: 30d). The tier\ncap is exposed via `mediaRetentionPolicy.bounds.maxRecordingRetentionDays`.'


class SetMediaRetentionPolicySetMediaRetentionPolicyMediaRetentionPolicy(
    MediaRetentionPolicyDefault
):
    typename__: Literal["MediaRetentionPolicy"] = Field(alias="__typename")


class SetMediaRetentionPolicySetMediaRetentionPolicyValidationError(
    ValidationErrorDefault
):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class SetMediaRetentionPolicySetMediaRetentionPolicyAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


SetMediaRetentionPolicy.model_rebuild()
