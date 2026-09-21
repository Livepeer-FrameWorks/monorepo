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


class RefreshStreamKey(BaseModel):
    refresh_stream_key: Annotated[
        Union[
            "RefreshStreamKeyRefreshStreamKeyStream",
            "RefreshStreamKeyRefreshStreamKeyValidationError",
            "RefreshStreamKeyRefreshStreamKeyNotFoundError",
            "RefreshStreamKeyRefreshStreamKeyAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(alias="refreshStreamKey")


class RefreshStreamKeyRefreshStreamKeyStream(StreamFields):
    typename__: Literal["Stream"] = Field(alias="__typename")


class RefreshStreamKeyRefreshStreamKeyValidationError(ValidationErrorFields):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class RefreshStreamKeyRefreshStreamKeyNotFoundError(NotFoundErrorFields):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class RefreshStreamKeyRefreshStreamKeyAuthError(AuthErrorFields):
    typename__: Literal["AuthError"] = Field(alias="__typename")


RefreshStreamKey.model_rebuild()
