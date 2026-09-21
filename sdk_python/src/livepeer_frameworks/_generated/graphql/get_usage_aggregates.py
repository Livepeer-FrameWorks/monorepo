from datetime import datetime
from typing import Optional

from pydantic import Field

from .base_model import BaseModel


class GetUsageAggregates(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    usage_aggregates: list["GetUsageAggregatesUsageAggregates"] = Field(
        alias="usageAggregates",
        description="Get aggregated usage data grouped by time interval.",
    )
    "Get aggregated usage data grouped by time interval."


class GetUsageAggregatesUsageAggregates(BaseModel):
    usage_type: str = Field(alias="usageType")
    period_start: Optional[datetime] = Field(alias="periodStart")
    period_end: Optional[datetime] = Field(alias="periodEnd")
    usage_value: float = Field(alias="usageValue")
    granularity: str


GetUsageAggregates.model_rebuild()
