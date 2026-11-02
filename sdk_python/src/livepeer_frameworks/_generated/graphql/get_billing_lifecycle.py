from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import (  # noqa: F401
    BillingLifecycleDefault,
    BillingLifecycleDefaultFreeFallback,
    BillingLifecycleDefaultOperatorGrant,
    BillingLifecycleDefaultPendingTier,
)


class GetBillingLifecycle(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    billing_lifecycle: Optional["GetBillingLifecycleBillingLifecycle"] = Field(
        alias="billingLifecycle",
        description="The current tenant's plan lifecycle: a FrameWorks arrangement, a scheduled\nmove to Free and how far a scheduled plan change has progressed.",
    )
    "The current tenant's plan lifecycle: a FrameWorks arrangement, a scheduled\nmove to Free and how far a scheduled plan change has progressed."


class GetBillingLifecycleBillingLifecycle(BillingLifecycleDefault):
    """A tenant's plan lifecycle, as its billing page shows it."""

    pass


GetBillingLifecycle.model_rebuild()
