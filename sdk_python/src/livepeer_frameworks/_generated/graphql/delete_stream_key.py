from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import AuthErrorFields, DeleteSuccessFields, NotFoundErrorFields


class DeleteStreamKey(BaseModel):
    delete_stream_key: Annotated[
        Union[
            "DeleteStreamKeyDeleteStreamKeyDeleteSuccess",
            "DeleteStreamKeyDeleteStreamKeyNotFoundError",
            "DeleteStreamKeyDeleteStreamKeyAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(alias="deleteStreamKey")


class DeleteStreamKeyDeleteStreamKeyDeleteSuccess(DeleteSuccessFields):
    typename__: Literal["DeleteSuccess"] = Field(alias="__typename")


class DeleteStreamKeyDeleteStreamKeyNotFoundError(NotFoundErrorFields):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class DeleteStreamKeyDeleteStreamKeyAuthError(AuthErrorFields):
    typename__: Literal["AuthError"] = Field(alias="__typename")


DeleteStreamKey.model_rebuild()
