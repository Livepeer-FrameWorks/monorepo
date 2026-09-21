from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import AuthErrorFields, DeleteSuccessFields, NotFoundErrorFields


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


class RevokeDeveloperTokenRevokeDeveloperTokenDeleteSuccess(DeleteSuccessFields):
    typename__: Literal["DeleteSuccess"] = Field(alias="__typename")


class RevokeDeveloperTokenRevokeDeveloperTokenNotFoundError(NotFoundErrorFields):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class RevokeDeveloperTokenRevokeDeveloperTokenAuthError(AuthErrorFields):
    typename__: Literal["AuthError"] = Field(alias="__typename")


RevokeDeveloperToken.model_rebuild()
