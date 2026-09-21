from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import AuthError, DeveloperToken, RateLimitError, ValidationError


class CreateDeveloperToken(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    create_developer_token: Annotated[
        Union[
            "CreateDeveloperTokenCreateDeveloperTokenDeveloperToken",
            "CreateDeveloperTokenCreateDeveloperTokenValidationError",
            "CreateDeveloperTokenCreateDeveloperTokenRateLimitError",
            "CreateDeveloperTokenCreateDeveloperTokenAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="createDeveloperToken",
        description="Create a new API token for programmatic access.",
    )
    "Create a new API token for programmatic access."


class CreateDeveloperTokenCreateDeveloperTokenDeveloperToken(DeveloperToken):
    typename__: Literal["DeveloperToken"] = Field(alias="__typename")


class CreateDeveloperTokenCreateDeveloperTokenValidationError(ValidationError):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class CreateDeveloperTokenCreateDeveloperTokenRateLimitError(RateLimitError):
    typename__: Literal["RateLimitError"] = Field(alias="__typename")


class CreateDeveloperTokenCreateDeveloperTokenAuthError(AuthError):
    typename__: Literal["AuthError"] = Field(alias="__typename")


CreateDeveloperToken.model_rebuild()
