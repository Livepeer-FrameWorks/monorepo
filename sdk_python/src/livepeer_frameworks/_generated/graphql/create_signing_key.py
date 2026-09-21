from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import AuthError, RateLimitError, SigningKey, ValidationError


class CreateSigningKey(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    create_signing_key: Annotated[
        Union[
            "CreateSigningKeyCreateSigningKeyCreateSigningKeySuccess",
            "CreateSigningKeyCreateSigningKeyValidationError",
            "CreateSigningKeyCreateSigningKeyRateLimitError",
            "CreateSigningKeyCreateSigningKeyAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="createSigningKey",
        description="Generate a new ES256 playback signing keypair. The private key is returned\nONCE in the response and never stored or returned again — capture it.\nUp to 10 active keys per tenant; revoke before re-creating.",
    )
    "Generate a new ES256 playback signing keypair. The private key is returned\nONCE in the response and never stored or returned again — capture it.\nUp to 10 active keys per tenant; revoke before re-creating."


class CreateSigningKeyCreateSigningKeyCreateSigningKeySuccess(BaseModel):
    typename__: Literal["CreateSigningKeySuccess"] = Field(alias="__typename")
    signing_key: "CreateSigningKeyCreateSigningKeyCreateSigningKeySuccessSigningKey" = (
        Field(alias="signingKey")
    )
    private_key_pem: str = Field(
        alias="privateKeyPem",
        description="ES256 private key in PEM format. Shown ONCE; never stored, never logged.",
    )
    "ES256 private key in PEM format. Shown ONCE; never stored, never logged."


class CreateSigningKeyCreateSigningKeyCreateSigningKeySuccessSigningKey(SigningKey):
    """A customer-managed signing key for issuing viewer playback JWTs. The private
    key is returned exactly once at creation time (in CreateSigningKeySuccess);
    FrameWorks stores only the public key. Up to 10 active keys per tenant."""

    pass


class CreateSigningKeyCreateSigningKeyValidationError(ValidationError):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class CreateSigningKeyCreateSigningKeyRateLimitError(RateLimitError):
    typename__: Literal["RateLimitError"] = Field(alias="__typename")


class CreateSigningKeyCreateSigningKeyAuthError(AuthError):
    typename__: Literal["AuthError"] = Field(alias="__typename")


CreateSigningKey.model_rebuild()
CreateSigningKeyCreateSigningKeyCreateSigningKeySuccess.model_rebuild()
