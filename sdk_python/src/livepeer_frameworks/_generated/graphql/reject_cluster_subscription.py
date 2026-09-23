from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (
    AuthErrorDefault,
    ClusterSubscriptionDefault,
    NotFoundErrorDefault,
    ValidationErrorDefault,
)


class RejectClusterSubscription(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    reject_cluster_subscription: Annotated[
        Union[
            "RejectClusterSubscriptionRejectClusterSubscriptionClusterSubscription",
            "RejectClusterSubscriptionRejectClusterSubscriptionValidationError",
            "RejectClusterSubscriptionRejectClusterSubscriptionNotFoundError",
            "RejectClusterSubscriptionRejectClusterSubscriptionAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="rejectClusterSubscription",
        description="Reject a pending cluster subscription request.",
    )
    "Reject a pending cluster subscription request."


class RejectClusterSubscriptionRejectClusterSubscriptionClusterSubscription(
    ClusterSubscriptionDefault
):
    typename__: Literal["ClusterSubscription"] = Field(alias="__typename")


class RejectClusterSubscriptionRejectClusterSubscriptionValidationError(
    ValidationErrorDefault
):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class RejectClusterSubscriptionRejectClusterSubscriptionNotFoundError(
    NotFoundErrorDefault
):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class RejectClusterSubscriptionRejectClusterSubscriptionAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


RejectClusterSubscription.model_rebuild()
