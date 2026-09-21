from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import AuthError, NotFoundError, SigningKey


class RevokeSigningKey(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    revoke_signing_key: Annotated[
        Union[
            "RevokeSigningKeyRevokeSigningKeySigningKey",
            "RevokeSigningKeyRevokeSigningKeyNotFoundError",
            "RevokeSigningKeyRevokeSigningKeyAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="revokeSigningKey",
        description="Mark an active signing key as revoked. Triggers session re-evaluation\nacross the tenant's protected playback objects: viewers with valid auth\ncontinue (possibly with a brief reconnect), revoked viewers are denied.",
    )
    "Mark an active signing key as revoked. Triggers session re-evaluation\nacross the tenant's protected playback objects: viewers with valid auth\ncontinue (possibly with a brief reconnect), revoked viewers are denied."


class RevokeSigningKeyRevokeSigningKeySigningKey(SigningKey):
    typename__: Literal["SigningKey"] = Field(alias="__typename")


class RevokeSigningKeyRevokeSigningKeyNotFoundError(NotFoundError):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class RevokeSigningKeyRevokeSigningKeyAuthError(AuthError):
    typename__: Literal["AuthError"] = Field(alias="__typename")


RevokeSigningKey.model_rebuild()
