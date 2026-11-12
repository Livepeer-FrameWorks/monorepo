from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (
    AuthErrorDefault,
    RevokeOtherAuthSessionsPayloadDefault,
    ValidationErrorDefault,
)


class RevokeOtherAuthSessions(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    revoke_other_auth_sessions: Annotated[
        Union[
            "RevokeOtherAuthSessionsRevokeOtherAuthSessionsRevokeOtherAuthSessionsPayload",
            "RevokeOtherAuthSessionsRevokeOtherAuthSessionsValidationError",
            "RevokeOtherAuthSessionsRevokeOtherAuthSessionsAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="revokeOtherAuthSessions",
        description="Sign out every other session while retaining the presented named session.",
    )
    "Sign out every other session while retaining the presented named session."


class RevokeOtherAuthSessionsRevokeOtherAuthSessionsRevokeOtherAuthSessionsPayload(
    RevokeOtherAuthSessionsPayloadDefault
):
    typename__: Literal["RevokeOtherAuthSessionsPayload"] = Field(alias="__typename")


class RevokeOtherAuthSessionsRevokeOtherAuthSessionsValidationError(
    ValidationErrorDefault
):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class RevokeOtherAuthSessionsRevokeOtherAuthSessionsAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


RevokeOtherAuthSessions.model_rebuild()
