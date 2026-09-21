from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (  # noqa: F401
    AuthErrorFields,
    NotFoundErrorFields,
    StreamFields,
    StreamFieldsMetrics,
    StreamFieldsPlaybackPolicy,
    StreamFieldsPullSource,
    ValidationErrorFields,
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


class UpdateStreamUpdateStreamStream(StreamFields):
    typename__: Literal["Stream"] = Field(alias="__typename")


class UpdateStreamUpdateStreamValidationError(ValidationErrorFields):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class UpdateStreamUpdateStreamNotFoundError(NotFoundErrorFields):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class UpdateStreamUpdateStreamAuthError(AuthErrorFields):
    typename__: Literal["AuthError"] = Field(alias="__typename")


UpdateStream.model_rebuild()
