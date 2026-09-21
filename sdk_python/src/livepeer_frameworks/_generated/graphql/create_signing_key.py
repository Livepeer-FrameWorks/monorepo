from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import AuthError, RateLimitError, SigningKey, ValidationError


class CreateSigningKey(BaseModel):
    create_signing_key: Annotated[
        Union[
            "CreateSigningKeyCreateSigningKeyCreateSigningKeySuccess",
            "CreateSigningKeyCreateSigningKeyValidationError",
            "CreateSigningKeyCreateSigningKeyRateLimitError",
            "CreateSigningKeyCreateSigningKeyAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(alias="createSigningKey")


class CreateSigningKeyCreateSigningKeyCreateSigningKeySuccess(BaseModel):
    typename__: Literal["CreateSigningKeySuccess"] = Field(alias="__typename")
    signing_key: "CreateSigningKeyCreateSigningKeyCreateSigningKeySuccessSigningKey" = (
        Field(alias="signingKey")
    )
    private_key_pem: str = Field(alias="privateKeyPem")


CreateSigningKeyCreateSigningKeyCreateSigningKeySuccessSigningKey = SigningKey


class CreateSigningKeyCreateSigningKeyValidationError(ValidationError):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class CreateSigningKeyCreateSigningKeyRateLimitError(RateLimitError):
    typename__: Literal["RateLimitError"] = Field(alias="__typename")


class CreateSigningKeyCreateSigningKeyAuthError(AuthError):
    typename__: Literal["AuthError"] = Field(alias="__typename")


CreateSigningKey.model_rebuild()
CreateSigningKeyCreateSigningKeyCreateSigningKeySuccess.model_rebuild()
