from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (  # noqa: F401
    AuthError,
    NotFoundError,
    Stream,
    StreamMetrics,
    StreamPlaybackPolicy,
    StreamPullSource,
    ValidationError,
)


class UpdateStream(BaseModel):
    update_stream: Annotated[
        Union[
            "UpdateStreamUpdateStreamStream",
            "UpdateStreamUpdateStreamValidationError",
            "UpdateStreamUpdateStreamNotFoundError",
            "UpdateStreamUpdateStreamAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(alias="updateStream")


class UpdateStreamUpdateStreamStream(Stream):
    typename__: Literal["Stream"] = Field(alias="__typename")


class UpdateStreamUpdateStreamValidationError(ValidationError):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class UpdateStreamUpdateStreamNotFoundError(NotFoundError):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class UpdateStreamUpdateStreamAuthError(AuthError):
    typename__: Literal["AuthError"] = Field(alias="__typename")


UpdateStream.model_rebuild()
