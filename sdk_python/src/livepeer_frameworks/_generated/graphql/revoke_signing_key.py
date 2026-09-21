from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import AuthErrorFields, NotFoundErrorFields, SigningKeyFields


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


class RevokeSigningKeyRevokeSigningKeySigningKey(SigningKeyFields):
    typename__: Literal["SigningKey"] = Field(alias="__typename")


class RevokeSigningKeyRevokeSigningKeyNotFoundError(NotFoundErrorFields):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class RevokeSigningKeyRevokeSigningKeyAuthError(AuthErrorFields):
    typename__: Literal["AuthError"] = Field(alias="__typename")


RevokeSigningKey.model_rebuild()
