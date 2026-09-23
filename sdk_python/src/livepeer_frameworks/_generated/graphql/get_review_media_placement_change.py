from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (  # noqa: F401
    AuthErrorDefault,
    MediaPlacementErrorInMediaPlacementReviewResultDefault,
    MediaPlacementErrorInMediaPlacementReviewResultDefaultFields,
    MediaPlacementReviewDefault,
    MediaPlacementReviewDefaultDifferences,
    MediaPlacementReviewDefaultImpact,
    MediaPlacementReviewDefaultWarnings,
    NotFoundErrorDefault,
)


class GetReviewMediaPlacementChange(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    review_media_placement_change: Annotated[
        Union[
            "GetReviewMediaPlacementChangeReviewMediaPlacementChangeMediaPlacementReview",
            "GetReviewMediaPlacementChangeReviewMediaPlacementChangeMediaPlacementError",
            "GetReviewMediaPlacementChangeReviewMediaPlacementChangeAuthError",
            "GetReviewMediaPlacementChangeReviewMediaPlacementChangeNotFoundError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(alias="reviewMediaPlacementChange")


class GetReviewMediaPlacementChangeReviewMediaPlacementChangeMediaPlacementReview(
    MediaPlacementReviewDefault
):
    typename__: Literal["MediaPlacementReview"] = Field(alias="__typename")


class GetReviewMediaPlacementChangeReviewMediaPlacementChangeMediaPlacementError(
    MediaPlacementErrorInMediaPlacementReviewResultDefault
):
    typename__: Literal["MediaPlacementError"] = Field(alias="__typename")


class GetReviewMediaPlacementChangeReviewMediaPlacementChangeAuthError(
    AuthErrorDefault
):
    typename__: Literal["AuthError"] = Field(alias="__typename")


class GetReviewMediaPlacementChangeReviewMediaPlacementChangeNotFoundError(
    NotFoundErrorDefault
):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


GetReviewMediaPlacementChange.model_rebuild()
