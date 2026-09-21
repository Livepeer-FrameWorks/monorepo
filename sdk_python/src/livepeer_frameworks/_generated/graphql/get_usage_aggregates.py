from datetime import datetime
from typing import Optional

from pydantic import Field

from .base_model import BaseModel


class GetUsageAggregates(BaseModel):
    usage_aggregates: list["GetUsageAggregatesUsageAggregates"] = Field(
        alias="usageAggregates"
    )


class GetUsageAggregatesUsageAggregates(BaseModel):
    usage_type: str = Field(alias="usageType")
    period_start: Optional[datetime] = Field(alias="periodStart")
    period_end: Optional[datetime] = Field(alias="periodEnd")
    usage_value: float = Field(alias="usageValue")
    granularity: str


GetUsageAggregates.model_rebuild()
