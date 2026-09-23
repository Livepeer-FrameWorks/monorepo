from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import (
    BillingDetailsDefault,
    BillingDetailsDefaultAddress,  # noqa: F401
)


class GetBillingDetails(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    billing_details: Optional["GetBillingDetailsBillingDetails"] = Field(
        alias="billingDetails",
        description="Get billing details for the current tenant.\nEmail and address are required before funding or postpaid setup. A VAT/tax\nidentifier is optional customer-supplied data and is not automatically verified.",
    )
    "Get billing details for the current tenant.\nEmail and address are required before funding or postpaid setup. A VAT/tax\nidentifier is optional customer-supplied data and is not automatically verified."


class GetBillingDetailsBillingDetails(BillingDetailsDefault):
    """Billing details for a tenant. These are optional for Free and small simplified
    top-ups, and required when paid postpaid collection or a full invoice needs them."""

    pass


GetBillingDetails.model_rebuild()
