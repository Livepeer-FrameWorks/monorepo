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


class ResetMediaRetentionOverride(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    reset_media_retention_override: Annotated[
        Union[
            "ResetMediaRetentionOverrideResetMediaRetentionOverrideEffectiveRetention",
            "ResetMediaRetentionOverrideResetMediaRetentionOverrideValidationError",
            "ResetMediaRetentionOverrideResetMediaRetentionOverrideNotFoundError",
            "ResetMediaRetentionOverrideResetMediaRetentionOverrideAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="resetMediaRetentionOverride",
        description="Clear a per-asset retention override and recompute the horizon from the\ntenant default (or tier entitlement when no tenant default is set).",
    )
    "Clear a per-asset retention override and recompute the horizon from the\ntenant default (or tier entitlement when no tenant default is set)."


class ResetMediaRetentionOverrideResetMediaRetentionOverrideEffectiveRetention(
    EffectiveRetentionDefault
):
    typename__: Literal["EffectiveRetention"] = Field(alias="__typename")


class ResetMediaRetentionOverrideResetMediaRetentionOverrideValidationError(
    ValidationErrorDefault
):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class ResetMediaRetentionOverrideResetMediaRetentionOverrideNotFoundError(
    NotFoundErrorDefault
):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class ResetMediaRetentionOverrideResetMediaRetentionOverrideAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


ResetMediaRetentionOverride.model_rebuild()
