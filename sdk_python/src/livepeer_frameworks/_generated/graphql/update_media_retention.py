from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (
    AuthErrorDefault,
    EffectiveRetentionDefault,
    NotFoundErrorDefault,
    ValidationErrorDefault,
)


class UpdateMediaRetention(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    update_media_retention: Annotated[
        Union[
            "UpdateMediaRetentionUpdateMediaRetentionEffectiveRetention",
            "UpdateMediaRetentionUpdateMediaRetentionValidationError",
            "UpdateMediaRetentionUpdateMediaRetentionNotFoundError",
            "UpdateMediaRetentionUpdateMediaRetentionAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="updateMediaRetention",
        description="Apply a per-asset retention override on a finalized DVR recording, clip,\nor VOD asset (set targetType accordingly). Override beats tenant default\nbeats tier entitlement. Active assets are rejected — retention applies\npost-finalize.",
    )
    "Apply a per-asset retention override on a finalized DVR recording, clip,\nor VOD asset (set targetType accordingly). Override beats tenant default\nbeats tier entitlement. Active assets are rejected — retention applies\npost-finalize."


class UpdateMediaRetentionUpdateMediaRetentionEffectiveRetention(
    EffectiveRetentionDefault
):
    typename__: Literal["EffectiveRetention"] = Field(alias="__typename")


class UpdateMediaRetentionUpdateMediaRetentionValidationError(ValidationErrorDefault):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class UpdateMediaRetentionUpdateMediaRetentionNotFoundError(NotFoundErrorDefault):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class UpdateMediaRetentionUpdateMediaRetentionAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


UpdateMediaRetention.model_rebuild()
