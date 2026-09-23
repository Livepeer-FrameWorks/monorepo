from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (  # noqa: F401
    AuthErrorDefault,
    MediaPlacementErrorInMediaPlacementPolicyResultDefault,
    MediaPlacementErrorInMediaPlacementPolicyResultDefaultFields,
    MediaPlacementPolicyStateDefault,
    MediaPlacementPolicyStateDefaultActions,
    MediaPlacementPolicyStateDefaultFeatures,
    MediaPlacementPolicyStateDefaultRollout,
    MediaPlacementPolicyStateDefaultScope,
    MediaPlacementPolicyStateDefaultVerbs,
    NotFoundErrorDefault,
)


class GetMediaPlacementPolicy(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    media_placement_policy: Annotated[
        Union[
            "GetMediaPlacementPolicyMediaPlacementPolicyMediaPlacementPolicyState",
            "GetMediaPlacementPolicyMediaPlacementPolicyMediaPlacementError",
            "GetMediaPlacementPolicyMediaPlacementPolicyAuthError",
            "GetMediaPlacementPolicyMediaPlacementPolicyNotFoundError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="mediaPlacementPolicy",
        description="Tenant-bound placement intent and actual enforcement progress.",
    )
    "Tenant-bound placement intent and actual enforcement progress."


class GetMediaPlacementPolicyMediaPlacementPolicyMediaPlacementPolicyState(
    MediaPlacementPolicyStateDefault
):
    typename__: Literal["MediaPlacementPolicyState"] = Field(alias="__typename")


class GetMediaPlacementPolicyMediaPlacementPolicyMediaPlacementError(
    MediaPlacementErrorInMediaPlacementPolicyResultDefault
):
    typename__: Literal["MediaPlacementError"] = Field(alias="__typename")


class GetMediaPlacementPolicyMediaPlacementPolicyAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


class GetMediaPlacementPolicyMediaPlacementPolicyNotFoundError(NotFoundErrorDefault):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


GetMediaPlacementPolicy.model_rebuild()
