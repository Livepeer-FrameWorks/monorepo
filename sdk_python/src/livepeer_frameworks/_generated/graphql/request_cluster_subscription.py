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


class RequestClusterSubscription(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    request_cluster_subscription: Annotated[
        Union[
            "RequestClusterSubscriptionRequestClusterSubscriptionClusterSubscription",
            "RequestClusterSubscriptionRequestClusterSubscriptionValidationError",
            "RequestClusterSubscriptionRequestClusterSubscriptionNotFoundError",
            "RequestClusterSubscriptionRequestClusterSubscriptionAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="requestClusterSubscription",
        description="Request to subscribe to a cluster.",
    )
    "Request to subscribe to a cluster."


class RequestClusterSubscriptionRequestClusterSubscriptionClusterSubscription(
    ClusterSubscriptionDefault
):
    typename__: Literal["ClusterSubscription"] = Field(alias="__typename")


class RequestClusterSubscriptionRequestClusterSubscriptionValidationError(
    ValidationErrorDefault
):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class RequestClusterSubscriptionRequestClusterSubscriptionNotFoundError(
    NotFoundErrorDefault
):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class RequestClusterSubscriptionRequestClusterSubscriptionAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


RequestClusterSubscription.model_rebuild()
