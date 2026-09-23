from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (
    AuthErrorDefault,
    NotFoundErrorDefault,
    StripeCheckoutSessionDefault,
    ValidationErrorDefault,
)


class CreateStripeCheckout(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    create_stripe_checkout: Annotated[
        Union[
            "CreateStripeCheckoutCreateStripeCheckoutStripeCheckoutSession",
            "CreateStripeCheckoutCreateStripeCheckoutValidationError",
            "CreateStripeCheckoutCreateStripeCheckoutNotFoundError",
            "CreateStripeCheckoutCreateStripeCheckoutAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="createStripeCheckout",
        description="Create a Stripe Checkout Session for subscription setup.\nReturns a URL to redirect the user to Stripe's hosted checkout page.\nAfter successful payment, user is redirected to successUrl.",
    )
    "Create a Stripe Checkout Session for subscription setup.\nReturns a URL to redirect the user to Stripe's hosted checkout page.\nAfter successful payment, user is redirected to successUrl."


class CreateStripeCheckoutCreateStripeCheckoutStripeCheckoutSession(
    StripeCheckoutSessionDefault
):
    typename__: Literal["StripeCheckoutSession"] = Field(alias="__typename")


class CreateStripeCheckoutCreateStripeCheckoutValidationError(ValidationErrorDefault):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class CreateStripeCheckoutCreateStripeCheckoutNotFoundError(NotFoundErrorDefault):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class CreateStripeCheckoutCreateStripeCheckoutAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


CreateStripeCheckout.model_rebuild()
