from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import AuthError, NotFoundError, SigningKey


class RevokeSigningKey(BaseModel):
    revoke_signing_key: Annotated[
        Union[
            "RevokeSigningKeyRevokeSigningKeySigningKey",
            "RevokeSigningKeyRevokeSigningKeyNotFoundError",
            "RevokeSigningKeyRevokeSigningKeyAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(alias="revokeSigningKey")


class RevokeSigningKeyRevokeSigningKeySigningKey(SigningKey):
    typename__: Literal["SigningKey"] = Field(alias="__typename")


class RevokeSigningKeyRevokeSigningKeyNotFoundError(NotFoundError):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class RevokeSigningKeyRevokeSigningKeyAuthError(AuthError):
    typename__: Literal["AuthError"] = Field(alias="__typename")


RevokeSigningKey.model_rebuild()
