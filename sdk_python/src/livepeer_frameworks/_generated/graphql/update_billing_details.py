from pydantic import Field

from .base_model import BaseModel
from .fragments import (
    BillingDetailsDefault,
    BillingDetailsDefaultAddress,  # noqa: F401
)


class UpdateBillingDetails(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    update_billing_details: "UpdateBillingDetailsUpdateBillingDetails" = Field(
        alias="updateBillingDetails",
        description="Update billing details for the current tenant.\nRequired before any payment for VAT invoicing.",
    )
    "Update billing details for the current tenant.\nRequired before any payment for VAT invoicing."


class UpdateBillingDetailsUpdateBillingDetails(BillingDetailsDefault):
    """Billing details for a tenant. These are optional for Free and small simplified
    top-ups, and required when paid postpaid collection or a full invoice needs them."""

    pass


UpdateBillingDetails.model_rebuild()
