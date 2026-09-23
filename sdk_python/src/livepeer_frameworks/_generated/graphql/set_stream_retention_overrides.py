from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (
    AuthErrorDefault,
    NotFoundErrorDefault,
    StreamRetentionOverridesDefault,
    ValidationErrorDefault,
)


class SetStreamRetentionOverrides(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    set_stream_retention_overrides: Annotated[
        Union[
            "SetStreamRetentionOverridesSetStreamRetentionOverridesStreamRetentionOverrides",
            "SetStreamRetentionOverridesSetStreamRetentionOverridesValidationError",
            "SetStreamRetentionOverridesSetStreamRetentionOverridesNotFoundError",
            "SetStreamRetentionOverridesSetStreamRetentionOverridesAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="setStreamRetentionOverrides",
        description="Write per-stream retention overrides for DVR and clip artifacts created\non this stream. Unspecified input fields are left alone; passing -1 on\na field clears the override (falls back to the tenant default). Values\nexceeding the tier cap are clamped at write time.",
    )
    "Write per-stream retention overrides for DVR and clip artifacts created\non this stream. Unspecified input fields are left alone; passing -1 on\na field clears the override (falls back to the tenant default). Values\nexceeding the tier cap are clamped at write time."


class SetStreamRetentionOverridesSetStreamRetentionOverridesStreamRetentionOverrides(
    StreamRetentionOverridesDefault
):
    typename__: Literal["StreamRetentionOverrides"] = Field(alias="__typename")


class SetStreamRetentionOverridesSetStreamRetentionOverridesValidationError(
    ValidationErrorDefault
):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class SetStreamRetentionOverridesSetStreamRetentionOverridesNotFoundError(
    NotFoundErrorDefault
):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class SetStreamRetentionOverridesSetStreamRetentionOverridesAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


SetStreamRetentionOverrides.model_rebuild()
