from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (
    AuthErrorFields,
    DeveloperTokenFields,
    RateLimitErrorFields,
    ValidationErrorFields,
)


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


class CreateDeveloperTokenCreateDeveloperTokenDeveloperToken(DeveloperTokenFields):
    typename__: Literal["DeveloperToken"] = Field(alias="__typename")


class CreateDeveloperTokenCreateDeveloperTokenValidationError(ValidationErrorFields):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class CreateDeveloperTokenCreateDeveloperTokenRateLimitError(RateLimitErrorFields):
    typename__: Literal["RateLimitError"] = Field(alias="__typename")


class CreateDeveloperTokenCreateDeveloperTokenAuthError(AuthErrorFields):
    typename__: Literal["AuthError"] = Field(alias="__typename")


CreateDeveloperToken.model_rebuild()
