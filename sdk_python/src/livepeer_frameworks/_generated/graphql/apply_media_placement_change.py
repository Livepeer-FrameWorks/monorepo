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


class ApplyMediaPlacementChange(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    apply_media_placement_change: Annotated[
        Union[
            "ApplyMediaPlacementChangeApplyMediaPlacementChangeMediaPlacementChange",
            "ApplyMediaPlacementChangeApplyMediaPlacementChangeMediaPlacementError",
            "ApplyMediaPlacementChangeApplyMediaPlacementChangeAuthError",
            "ApplyMediaPlacementChangeApplyMediaPlacementChangeNotFoundError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="applyMediaPlacementChange",
        description="Atomically saves the requested verbs. Saved does not mean effective on every cell.",
    )
    "Atomically saves the requested verbs. Saved does not mean effective on every cell."


class ApplyMediaPlacementChangeApplyMediaPlacementChangeMediaPlacementChange(
    MediaPlacementChangeDefault
):
    typename__: Literal["MediaPlacementChange"] = Field(alias="__typename")


class ApplyMediaPlacementChangeApplyMediaPlacementChangeMediaPlacementError(
    MediaPlacementErrorInMediaPlacementChangeResultDefault
):
    typename__: Literal["MediaPlacementError"] = Field(alias="__typename")


class ApplyMediaPlacementChangeApplyMediaPlacementChangeAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


class ApplyMediaPlacementChangeApplyMediaPlacementChangeNotFoundError(
    NotFoundErrorDefault
):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


ApplyMediaPlacementChange.model_rebuild()
