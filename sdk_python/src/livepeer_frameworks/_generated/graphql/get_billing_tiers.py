from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import (  # noqa: F401
    BillingTierDefault,
    BillingTierDefaultEntitlements,
    BillingTierDefaultFeatures,
    BillingTierDefaultPricingRules,
)


class GetBillingTiers(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    billing_tiers: Optional[list["GetBillingTiersBillingTiers"]] = Field(
        alias="billingTiers",
        description="List available billing tiers and their pricing.",
    )
    "List available billing tiers and their pricing."


class GetBillingTiersBillingTiers(BillingTierDefault):
    """A subscription tier defining pricing rules, entitlements, and features.
    Tenants subscribe to a tier which determines their billing and capabilities."""

    pass


GetBillingTiers.model_rebuild()
