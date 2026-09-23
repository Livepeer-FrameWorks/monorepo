from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (  # noqa: F401
    AuthErrorDefault,
    MediaPlacementChangeDefault,
    MediaPlacementChangeDefaultRollout,
    MediaPlacementChangeDefaultScope,
    MediaPlacementErrorInMediaPlacementChangeResultDefault,
    MediaPlacementErrorInMediaPlacementChangeResultDefaultFields,
    NotFoundErrorDefault,
)


class GetMediaPlacementChange(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    media_placement_change: Annotated[
        Union[
            "GetMediaPlacementChangeMediaPlacementChangeMediaPlacementChange",
            "GetMediaPlacementChangeMediaPlacementChangeMediaPlacementError",
            "GetMediaPlacementChangeMediaPlacementChangeAuthError",
            "GetMediaPlacementChangeMediaPlacementChangeNotFoundError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(alias="mediaPlacementChange")


class GetMediaPlacementChangeMediaPlacementChangeMediaPlacementChange(
    MediaPlacementChangeDefault
):
    typename__: Literal["MediaPlacementChange"] = Field(alias="__typename")


class GetMediaPlacementChangeMediaPlacementChangeMediaPlacementError(
    MediaPlacementErrorInMediaPlacementChangeResultDefault
):
    typename__: Literal["MediaPlacementError"] = Field(alias="__typename")


class GetMediaPlacementChangeMediaPlacementChangeAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


class GetMediaPlacementChangeMediaPlacementChangeNotFoundError(NotFoundErrorDefault):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


GetMediaPlacementChange.model_rebuild()
