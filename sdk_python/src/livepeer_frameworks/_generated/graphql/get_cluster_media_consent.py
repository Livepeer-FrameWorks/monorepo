from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (  # noqa: F401
    AuthErrorDefault,
    MediaCapacityConsentDefault,
    MediaCapacityConsentDefaultRollout,
    MediaPlacementErrorInMediaCapacityConsentResultDefault,
    MediaPlacementErrorInMediaCapacityConsentResultDefaultFields,
    NotFoundErrorDefault,
)


class GetClusterMediaConsent(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    cluster_media_consent: Annotated[
        Union[
            "GetClusterMediaConsentClusterMediaConsentMediaCapacityConsent",
            "GetClusterMediaConsentClusterMediaConsentMediaPlacementError",
            "GetClusterMediaConsentClusterMediaConsentAuthError",
            "GetClusterMediaConsentClusterMediaConsentNotFoundError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(alias="clusterMediaConsent")


class GetClusterMediaConsentClusterMediaConsentMediaCapacityConsent(
    MediaCapacityConsentDefault
):
    typename__: Literal["MediaCapacityConsent"] = Field(alias="__typename")


class GetClusterMediaConsentClusterMediaConsentMediaPlacementError(
    MediaPlacementErrorInMediaCapacityConsentResultDefault
):
    typename__: Literal["MediaPlacementError"] = Field(alias="__typename")


class GetClusterMediaConsentClusterMediaConsentAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


class GetClusterMediaConsentClusterMediaConsentNotFoundError(NotFoundErrorDefault):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


GetClusterMediaConsent.model_rebuild()
