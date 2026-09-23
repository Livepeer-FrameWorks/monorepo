from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import BillingStatusDefault


class GetBillingStatus(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    billing_status: Optional["GetBillingStatusBillingStatus"] = Field(
        alias="billingStatus",
        description="Get the current billing status including subscription tier and usage.",
    )
    "Get the current billing status including subscription tier and usage."


GetBillingStatusBillingStatus = BillingStatusDefault
GetBillingStatus.model_rebuild()
