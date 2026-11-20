from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (
    AuthErrorDefault,
    TranscodeLadderPolicyDefault,
    TranscodeLadderPolicyDefaultLadders,  # noqa: F401
    ValidationErrorDefault,
)


class SetTranscodeLadderPolicy(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    set_transcode_ladder_policy: Annotated[
        Union[
            "SetTranscodeLadderPolicySetTranscodeLadderPolicyTranscodeLadderPolicy",
            "SetTranscodeLadderPolicySetTranscodeLadderPolicyValidationError",
            "SetTranscodeLadderPolicySetTranscodeLadderPolicyAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="setTranscodeLadderPolicy",
        description="Set the account's video ladder. Empty or null inherits the plan default.",
    )
    "Set the account's video ladder. Empty or null inherits the plan default."


class SetTranscodeLadderPolicySetTranscodeLadderPolicyTranscodeLadderPolicy(
    TranscodeLadderPolicyDefault
):
    typename__: Literal["TranscodeLadderPolicy"] = Field(alias="__typename")


class SetTranscodeLadderPolicySetTranscodeLadderPolicyValidationError(
    ValidationErrorDefault
):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class SetTranscodeLadderPolicySetTranscodeLadderPolicyAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


SetTranscodeLadderPolicy.model_rebuild()
