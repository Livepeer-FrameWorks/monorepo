from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import AuthError, DeveloperToken, RateLimitError, ValidationError


class CreateDeveloperToken(BaseModel):
    create_developer_token: Annotated[
        Union[
            "CreateDeveloperTokenCreateDeveloperTokenDeveloperToken",
            "CreateDeveloperTokenCreateDeveloperTokenValidationError",
            "CreateDeveloperTokenCreateDeveloperTokenRateLimitError",
            "CreateDeveloperTokenCreateDeveloperTokenAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(alias="createDeveloperToken")


class CreateDeveloperTokenCreateDeveloperTokenDeveloperToken(DeveloperToken):
    typename__: Literal["DeveloperToken"] = Field(alias="__typename")


class CreateDeveloperTokenCreateDeveloperTokenValidationError(ValidationError):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class CreateDeveloperTokenCreateDeveloperTokenRateLimitError(RateLimitError):
    typename__: Literal["RateLimitError"] = Field(alias="__typename")


class CreateDeveloperTokenCreateDeveloperTokenAuthError(AuthError):
    typename__: Literal["AuthError"] = Field(alias="__typename")


CreateDeveloperToken.model_rebuild()
