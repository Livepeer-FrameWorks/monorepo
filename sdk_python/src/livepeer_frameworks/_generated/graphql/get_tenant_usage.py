from typing import Optional

from pydantic import Field

from .base_model import BaseModel


class GetTenantUsage(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    tenant_usage: Optional["GetTenantUsageTenantUsage"] = Field(
        alias="tenantUsage", description="Get aggregated usage metrics for the tenant."
    )
    "Get aggregated usage metrics for the tenant."


class GetTenantUsageTenantUsage(BaseModel):
    billing_period: str = Field(alias="billingPeriod")
    currency: str
    total_cost: float = Field(alias="totalCost")
    base_amount: str = Field(
        alias="baseAmount", description="Decimal-string base subscription portion."
    )
    "Decimal-string base subscription portion."
    usage_amount: str = Field(
        alias="usageAmount", description="Decimal-string metered portion."
    )
    "Decimal-string metered portion."
    usage: list["GetTenantUsageTenantUsageUsage"]
    costs: list["GetTenantUsageTenantUsageCosts"]
    line_items: list["GetTenantUsageTenantUsageLineItems"] = Field(
        alias="lineItems",
        description="Rated line items for the period (rating engine output).",
    )
    "Rated line items for the period (rating engine output)."


class GetTenantUsageTenantUsageUsage(BaseModel):
    resource_type: str = Field(alias="resourceType")
    amount: float


class GetTenantUsageTenantUsageCosts(BaseModel):
    resource_type: str = Field(alias="resourceType")
    cost: float


class GetTenantUsageTenantUsageLineItems(BaseModel):
    """A single line item on an invoice or usage preview, produced by the rating engine.
    Decimal quantities are strings to preserve precision."""

    line_key: str = Field(
        alias="lineKey",
        description="Stable identity: 'base_subscription', 'meter:<name>:<cluster_id>:<yyyymm>'.",
    )
    "Stable identity: 'base_subscription', 'meter:<name>:<cluster_id>:<yyyymm>'."
    meter: str = Field(description="Meter name; empty for base_subscription.")
    "Meter name; empty for base_subscription."
    description: str = Field(description="Description of the charge.")
    "Description of the charge."
    quantity: str = Field(description="Total quantity used (decimal as string).")
    "Total quantity used (decimal as string)."
    included_quantity: str = Field(
        alias="includedQuantity", description="Free quantity (decimal as string)."
    )
    "Free quantity (decimal as string)."
    billable_quantity: str = Field(
        alias="billableQuantity", description="Billable quantity (decimal as string)."
    )
    "Billable quantity (decimal as string)."
    unit_price: str = Field(
        alias="unitPrice", description="Price per unit (decimal as string)."
    )
    "Price per unit (decimal as string)."
    total: str = Field(
        description="Total for this line item (= billableQuantity * unitPrice), decimal as string."
    )
    "Total for this line item (= billableQuantity * unitPrice), decimal as string."
    currency: str = Field(description="Currency (ISO 4217).")
    "Currency (ISO 4217)."
    cluster_id: Optional[str] = Field(
        alias="clusterId",
        description="Cluster id this line was attributed to; null for tenant-scoped lines (base_subscription).",
    )
    "Cluster id this line was attributed to; null for tenant-scoped lines (base_subscription)."
    cluster_name: Optional[str] = Field(
        alias="clusterName",
        description="Display name of the cluster, joined from Quartermaster at read time. Null when clusterId is null.",
    )
    "Display name of the cluster, joined from Quartermaster at read time. Null when clusterId is null."
    pricing_source: str = Field(
        alias="pricingSource",
        description="Why the line was priced as it was: tier, cluster_metered, cluster_monthly, cluster_custom, free_unmetered, self_hosted, included_subscription.",
    )
    "Why the line was priced as it was: tier, cluster_metered, cluster_monthly, cluster_custom, free_unmetered, self_hosted, included_subscription."
    pricing_label: str = Field(
        alias="pricingLabel",
        description="Human-readable label of pricingSource (e.g. 'Self-hosted (no charge)', 'Marketplace metered'). Empty when not populated by the gateway.",
    )
    "Human-readable label of pricingSource (e.g. 'Self-hosted (no charge)', 'Marketplace metered'). Empty when not populated by the gateway."
    unit: str = Field(description="Canonical quantity unit.")
    "Canonical quantity unit."


GetTenantUsage.model_rebuild()
GetTenantUsageTenantUsage.model_rebuild()
