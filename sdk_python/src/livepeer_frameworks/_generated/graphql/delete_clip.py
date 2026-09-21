from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import AuthErrorFields, DeleteSuccessFields, NotFoundErrorFields


class DeleteClip(BaseModel):
    delete_clip: Annotated[
        Union[
            "DeleteClipDeleteClipDeleteSuccess",
            "DeleteClipDeleteClipNotFoundError",
            "DeleteClipDeleteClipAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(alias="deleteClip")


class DeleteClipDeleteClipDeleteSuccess(DeleteSuccessFields):
    typename__: Literal["DeleteSuccess"] = Field(alias="__typename")


class DeleteClipDeleteClipNotFoundError(NotFoundErrorFields):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class DeleteClipDeleteClipAuthError(AuthErrorFields):
    typename__: Literal["AuthError"] = Field(alias="__typename")


DeleteClip.model_rebuild()
