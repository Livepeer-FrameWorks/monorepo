from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import AuthError, DeleteSuccess, NotFoundError


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


class DeleteClipDeleteClipDeleteSuccess(DeleteSuccess):
    typename__: Literal["DeleteSuccess"] = Field(alias="__typename")


class DeleteClipDeleteClipNotFoundError(NotFoundError):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class DeleteClipDeleteClipAuthError(AuthError):
    typename__: Literal["AuthError"] = Field(alias="__typename")


DeleteClip.model_rebuild()
