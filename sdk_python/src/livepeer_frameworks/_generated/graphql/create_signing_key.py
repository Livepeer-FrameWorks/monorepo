from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (
    AuthErrorFields,
    RateLimitErrorFields,
    SigningKeyFields,
    ValidationErrorFields,
)


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


CreateSigningKeyCreateSigningKeyCreateSigningKeySuccessSigningKey = SigningKeyFields


class CreateSigningKeyCreateSigningKeyValidationError(ValidationErrorFields):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class CreateSigningKeyCreateSigningKeyRateLimitError(RateLimitErrorFields):
    typename__: Literal["RateLimitError"] = Field(alias="__typename")


class CreateSigningKeyCreateSigningKeyAuthError(AuthErrorFields):
    typename__: Literal["AuthError"] = Field(alias="__typename")


CreateSigningKey.model_rebuild()
CreateSigningKeyCreateSigningKeyCreateSigningKeySuccess.model_rebuild()
