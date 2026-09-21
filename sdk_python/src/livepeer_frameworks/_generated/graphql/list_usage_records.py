from datetime import datetime
from typing import Any, Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import PageInfo


class ListUsageRecords(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    usage_records_connection: "ListUsageRecordsUsageRecordsConnection" = Field(
        alias="usageRecordsConnection",
        description="List detailed usage records with pagination.",
    )
    "List detailed usage records with pagination."


class ListUsageRecordsUsageRecordsConnection(BaseModel):
    nodes: list["ListUsageRecordsUsageRecordsConnectionNodes"]
    page_info: "ListUsageRecordsUsageRecordsConnectionPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


class ListUsageRecordsUsageRecordsConnectionNodes(BaseModel):
    id: str
    cluster_id: Optional[str] = Field(alias="clusterId")
    cluster_name: Optional[str] = Field(alias="clusterName")
    usage_type: str = Field(alias="usageType")
    unit: str = Field(description="Canonical physical unit for usageValue.")
    "Canonical physical unit for usageValue."
    dimensions: Any = Field(
        description="Bounded dimensions that distinguish records for the same meter."
    )
    "Bounded dimensions that distinguish records for the same meter."
    usage_value: float = Field(alias="usageValue")
    created_at: Optional[datetime] = Field(alias="createdAt")
    period_start: Optional[datetime] = Field(alias="periodStart")
    period_end: Optional[datetime] = Field(alias="periodEnd")
    granularity: Optional[str]


ListUsageRecordsUsageRecordsConnectionPageInfo = PageInfo
ListUsageRecords.model_rebuild()
ListUsageRecordsUsageRecordsConnection.model_rebuild()
