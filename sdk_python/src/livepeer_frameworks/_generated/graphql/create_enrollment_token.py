from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (
    AuthErrorDefault,
    CreateEnrollmentTokenResponseDefault,
    CreateEnrollmentTokenResponseDefaultBootstrapToken,  # noqa: F401
    ValidationErrorDefault,
)


class CreateEnrollmentToken(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    create_enrollment_token: Annotated[
        Union[
            "CreateEnrollmentTokenCreateEnrollmentTokenCreateEnrollmentTokenResponse",
            "CreateEnrollmentTokenCreateEnrollmentTokenValidationError",
            "CreateEnrollmentTokenCreateEnrollmentTokenAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="createEnrollmentToken",
        description="Create an enrollment token for an existing cluster.\nRequires active subscription to the cluster.",
    )
    "Create an enrollment token for an existing cluster.\nRequires active subscription to the cluster."


class CreateEnrollmentTokenCreateEnrollmentTokenCreateEnrollmentTokenResponse(
    CreateEnrollmentTokenResponseDefault
):
    typename__: Literal["CreateEnrollmentTokenResponse"] = Field(alias="__typename")


class CreateEnrollmentTokenCreateEnrollmentTokenValidationError(ValidationErrorDefault):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class CreateEnrollmentTokenCreateEnrollmentTokenAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


CreateEnrollmentToken.model_rebuild()
