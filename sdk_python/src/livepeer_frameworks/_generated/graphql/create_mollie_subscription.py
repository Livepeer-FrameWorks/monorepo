from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (
    AuthErrorDefault,
    MollieSubscriptionDefault,
    NotFoundErrorDefault,
    ValidationErrorDefault,
)


class CreateMollieSubscription(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    create_mollie_subscription: Annotated[
        Union[
            "CreateMollieSubscriptionCreateMollieSubscriptionMollieSubscription",
            "CreateMollieSubscriptionCreateMollieSubscriptionValidationError",
            "CreateMollieSubscriptionCreateMollieSubscriptionNotFoundError",
            "CreateMollieSubscriptionCreateMollieSubscriptionAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="createMollieSubscription",
        description="Create a Mollie subscription after mandate is valid.\nCall this after the first payment webhook confirms mandate creation.",
    )
    "Create a Mollie subscription after mandate is valid.\nCall this after the first payment webhook confirms mandate creation."


class CreateMollieSubscriptionCreateMollieSubscriptionMollieSubscription(
    MollieSubscriptionDefault
):
    typename__: Literal["MollieSubscription"] = Field(alias="__typename")


class CreateMollieSubscriptionCreateMollieSubscriptionValidationError(
    ValidationErrorDefault
):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class CreateMollieSubscriptionCreateMollieSubscriptionNotFoundError(
    NotFoundErrorDefault
):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class CreateMollieSubscriptionCreateMollieSubscriptionAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


CreateMollieSubscription.model_rebuild()
