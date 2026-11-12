from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (
    AuthErrorDefault,
    NotFoundErrorDefault,
    RevokeAuthSessionPayloadDefault,
    ValidationErrorDefault,
)


class RevokeAuthSession(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    revoke_auth_session: Annotated[
        Union[
            "RevokeAuthSessionRevokeAuthSessionRevokeAuthSessionPayload",
            "RevokeAuthSessionRevokeAuthSessionValidationError",
            "RevokeAuthSessionRevokeAuthSessionNotFoundError",
            "RevokeAuthSessionRevokeAuthSessionAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="revokeAuthSession",
        description="Sign out one of the signed-in user’s own sessions. Idempotent; current=true means this caller was signed out.",
    )
    "Sign out one of the signed-in user’s own sessions. Idempotent; current=true means this caller was signed out."


class RevokeAuthSessionRevokeAuthSessionRevokeAuthSessionPayload(
    RevokeAuthSessionPayloadDefault
):
    typename__: Literal["RevokeAuthSessionPayload"] = Field(alias="__typename")


class RevokeAuthSessionRevokeAuthSessionValidationError(ValidationErrorDefault):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class RevokeAuthSessionRevokeAuthSessionNotFoundError(NotFoundErrorDefault):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class RevokeAuthSessionRevokeAuthSessionAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


RevokeAuthSession.model_rebuild()
