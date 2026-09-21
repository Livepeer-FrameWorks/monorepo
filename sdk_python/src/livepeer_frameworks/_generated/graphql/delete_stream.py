from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import AuthErrorFields, DeleteSuccessFields, NotFoundErrorFields


class DeleteStream(BaseModel):
    delete_stream: Annotated[
        Union[
            "DeleteStreamDeleteStreamDeleteSuccess",
            "DeleteStreamDeleteStreamNotFoundError",
            "DeleteStreamDeleteStreamAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(alias="deleteStream")


class DeleteStreamDeleteStreamDeleteSuccess(DeleteSuccessFields):
    typename__: Literal["DeleteSuccess"] = Field(alias="__typename")


class DeleteStreamDeleteStreamNotFoundError(NotFoundErrorFields):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class DeleteStreamDeleteStreamAuthError(AuthErrorFields):
    typename__: Literal["AuthError"] = Field(alias="__typename")


DeleteStream.model_rebuild()
