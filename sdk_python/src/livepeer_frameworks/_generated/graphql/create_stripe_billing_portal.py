from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (
    AuthErrorDefault,
    NotFoundErrorDefault,
    StripeBillingPortalSessionDefault,
    ValidationErrorDefault,
)


class CreateStripeBillingPortal(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    create_stripe_billing_portal: Annotated[
        Union[
            "CreateStripeBillingPortalCreateStripeBillingPortalStripeBillingPortalSession",
            "CreateStripeBillingPortalCreateStripeBillingPortalValidationError",
            "CreateStripeBillingPortalCreateStripeBillingPortalNotFoundError",
            "CreateStripeBillingPortalCreateStripeBillingPortalAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="createStripeBillingPortal",
        description="Create a Stripe Billing Portal session.\nReturns a URL to redirect the user to manage their subscription.",
    )
    "Create a Stripe Billing Portal session.\nReturns a URL to redirect the user to manage their subscription."


class CreateStripeBillingPortalCreateStripeBillingPortalStripeBillingPortalSession(
    StripeBillingPortalSessionDefault
):
    typename__: Literal["StripeBillingPortalSession"] = Field(alias="__typename")


class CreateStripeBillingPortalCreateStripeBillingPortalValidationError(
    ValidationErrorDefault
):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class CreateStripeBillingPortalCreateStripeBillingPortalNotFoundError(
    NotFoundErrorDefault
):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class CreateStripeBillingPortalCreateStripeBillingPortalAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


CreateStripeBillingPortal.model_rebuild()
