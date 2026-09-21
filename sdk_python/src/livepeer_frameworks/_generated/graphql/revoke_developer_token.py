from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import AuthError, DeleteSuccess, NotFoundError


class RevokeDeveloperToken(BaseModel):
    revoke_developer_token: Annotated[
        Union[
            "RevokeDeveloperTokenRevokeDeveloperTokenDeleteSuccess",
            "RevokeDeveloperTokenRevokeDeveloperTokenNotFoundError",
            "RevokeDeveloperTokenRevokeDeveloperTokenAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(alias="revokeDeveloperToken")


class RevokeDeveloperTokenRevokeDeveloperTokenDeleteSuccess(DeleteSuccess):
    typename__: Literal["DeleteSuccess"] = Field(alias="__typename")


class RevokeDeveloperTokenRevokeDeveloperTokenNotFoundError(NotFoundError):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class RevokeDeveloperTokenRevokeDeveloperTokenAuthError(AuthError):
    typename__: Literal["AuthError"] = Field(alias="__typename")


RevokeDeveloperToken.model_rebuild()
