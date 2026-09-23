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


class ApproveClusterSubscription(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    approve_cluster_subscription: Annotated[
        Union[
            "ApproveClusterSubscriptionApproveClusterSubscriptionClusterSubscription",
            "ApproveClusterSubscriptionApproveClusterSubscriptionValidationError",
            "ApproveClusterSubscriptionApproveClusterSubscriptionNotFoundError",
            "ApproveClusterSubscriptionApproveClusterSubscriptionAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="approveClusterSubscription",
        description="Approve a pending cluster subscription request.",
    )
    "Approve a pending cluster subscription request."


class ApproveClusterSubscriptionApproveClusterSubscriptionClusterSubscription(
    ClusterSubscriptionDefault
):
    typename__: Literal["ClusterSubscription"] = Field(alias="__typename")


class ApproveClusterSubscriptionApproveClusterSubscriptionValidationError(
    ValidationErrorDefault
):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class ApproveClusterSubscriptionApproveClusterSubscriptionNotFoundError(
    NotFoundErrorDefault
):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class ApproveClusterSubscriptionApproveClusterSubscriptionAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


ApproveClusterSubscription.model_rebuild()
