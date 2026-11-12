from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (
    AuthErrorDefault,
    ChangePasswordPayloadDefault,
    ValidationErrorDefault,
)


class ChangePassword(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    change_password: Annotated[
        Union[
            "ChangePasswordChangePasswordChangePasswordPayload",
            "ChangePasswordChangePasswordValidationError",
            "ChangePasswordChangePasswordAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="changePassword",
        description="Change the password and sign out other sessions, retaining this session.",
    )
    "Change the password and sign out other sessions, retaining this session."


class ChangePasswordChangePasswordChangePasswordPayload(ChangePasswordPayloadDefault):
    typename__: Literal["ChangePasswordPayload"] = Field(alias="__typename")


class ChangePasswordChangePasswordValidationError(ValidationErrorDefault):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class ChangePasswordChangePasswordAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


ChangePassword.model_rebuild()
