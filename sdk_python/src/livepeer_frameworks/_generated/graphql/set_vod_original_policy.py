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


class SetVodOriginalPolicy(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    set_vod_original_policy: Annotated[
        Union[
            "SetVodOriginalPolicySetVodOriginalPolicyMediaRetentionPolicy",
            "SetVodOriginalPolicySetVodOriginalPolicyValidationError",
            "SetVodOriginalPolicySetVodOriginalPolicyAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="setVodOriginalPolicy",
        description="Set the default for retaining original uploads. Null inherits keep=true. Existing assets are unchanged.",
    )
    "Set the default for retaining original uploads. Null inherits keep=true. Existing assets are unchanged."


class SetVodOriginalPolicySetVodOriginalPolicyMediaRetentionPolicy(
    MediaRetentionPolicyDefault
):
    typename__: Literal["MediaRetentionPolicy"] = Field(alias="__typename")


class SetVodOriginalPolicySetVodOriginalPolicyValidationError(ValidationErrorDefault):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class SetVodOriginalPolicySetVodOriginalPolicyAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


SetVodOriginalPolicy.model_rebuild()
