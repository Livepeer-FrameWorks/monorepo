from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (
    AuthErrorDefault,
    AuthSessionDefault,
    NotFoundErrorDefault,
    ValidationErrorDefault,
)


class RenameAuthSession(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    rename_auth_session: Annotated[
        Union[
            "RenameAuthSessionRenameAuthSessionAuthSession",
            "RenameAuthSessionRenameAuthSessionValidationError",
            "RenameAuthSessionRenameAuthSessionNotFoundError",
            "RenameAuthSessionRenameAuthSessionAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="renameAuthSession",
        description="Rename one of the signed-in user’s own sessions.",
    )
    "Rename one of the signed-in user’s own sessions."


class RenameAuthSessionRenameAuthSessionAuthSession(AuthSessionDefault):
    typename__: Literal["AuthSession"] = Field(alias="__typename")


class RenameAuthSessionRenameAuthSessionValidationError(ValidationErrorDefault):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class RenameAuthSessionRenameAuthSessionNotFoundError(NotFoundErrorDefault):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class RenameAuthSessionRenameAuthSessionAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


RenameAuthSession.model_rebuild()
