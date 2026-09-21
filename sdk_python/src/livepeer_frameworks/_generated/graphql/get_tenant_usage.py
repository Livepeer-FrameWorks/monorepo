from typing import Optional

from pydantic import Field

from .base_model import BaseModel


class GetTenantUsage(BaseModel):
    tenant_usage: Optional["GetTenantUsageTenantUsage"] = Field(alias="tenantUsage")


class GetTenantUsageTenantUsage(BaseModel):
    billing_period: str = Field(alias="billingPeriod")
    currency: str
    total_cost: float = Field(alias="totalCost")
    base_amount: str = Field(alias="baseAmount")
    usage_amount: str = Field(alias="usageAmount")
    usage: list["GetTenantUsageTenantUsageUsage"]
    costs: list["GetTenantUsageTenantUsageCosts"]
    line_items: list["GetTenantUsageTenantUsageLineItems"] = Field(alias="lineItems")


class GetTenantUsageTenantUsageUsage(BaseModel):
    resource_type: str = Field(alias="resourceType")
    amount: float


class GetTenantUsageTenantUsageCosts(BaseModel):
    resource_type: str = Field(alias="resourceType")
    cost: float


class GetTenantUsageTenantUsageLineItems(BaseModel):
    line_key: str = Field(alias="lineKey")
    meter: str
    description: str
    quantity: str
    included_quantity: str = Field(alias="includedQuantity")
    billable_quantity: str = Field(alias="billableQuantity")
    unit_price: str = Field(alias="unitPrice")
    total: str
    currency: str
    cluster_id: Optional[str] = Field(alias="clusterId")
    cluster_name: Optional[str] = Field(alias="clusterName")
    pricing_source: str = Field(alias="pricingSource")
    pricing_label: str = Field(alias="pricingLabel")
    unit: str


GetTenantUsage.model_rebuild()
GetTenantUsageTenantUsage.model_rebuild()
