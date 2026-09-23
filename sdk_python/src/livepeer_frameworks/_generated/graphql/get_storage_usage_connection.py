from pydantic import Field

from .base_model import BaseModel
from .fragments import PageInfoDefault, StorageUsageRecordDefault


class GetStorageUsageConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetStorageUsageConnectionAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetStorageUsageConnectionAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    usage: "GetStorageUsageConnectionAnalyticsUsage" = Field(
        description="Usage analytics: streaming hours, storage, and processing."
    )
    "Usage analytics: streaming hours, storage, and processing."


class GetStorageUsageConnectionAnalyticsUsage(BaseModel):
    """Usage analytics grouped by type."""

    storage: "GetStorageUsageConnectionAnalyticsUsageStorage" = Field(
        description="Storage usage: disk utilization by node and scope."
    )
    "Storage usage: disk utilization by node and scope."


class GetStorageUsageConnectionAnalyticsUsageStorage(BaseModel):
    storage_usage_connection: "GetStorageUsageConnectionAnalyticsUsageStorageStorageUsageConnection" = Field(
        alias="storageUsageConnection"
    )


class GetStorageUsageConnectionAnalyticsUsageStorageStorageUsageConnection(BaseModel):
    edges: list[
        "GetStorageUsageConnectionAnalyticsUsageStorageStorageUsageConnectionEdges"
    ]
    page_info: "GetStorageUsageConnectionAnalyticsUsageStorageStorageUsageConnectionPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


class GetStorageUsageConnectionAnalyticsUsageStorageStorageUsageConnectionEdges(
    BaseModel
):
    cursor: str
    node: (
        "GetStorageUsageConnectionAnalyticsUsageStorageStorageUsageConnectionEdgesNode"
    )


GetStorageUsageConnectionAnalyticsUsageStorageStorageUsageConnectionEdgesNode = (
    StorageUsageRecordDefault
)
GetStorageUsageConnectionAnalyticsUsageStorageStorageUsageConnectionPageInfo = (
    PageInfoDefault
)
GetStorageUsageConnection.model_rebuild()
GetStorageUsageConnectionAnalytics.model_rebuild()
GetStorageUsageConnectionAnalyticsUsage.model_rebuild()
GetStorageUsageConnectionAnalyticsUsageStorage.model_rebuild()
GetStorageUsageConnectionAnalyticsUsageStorageStorageUsageConnection.model_rebuild()
GetStorageUsageConnectionAnalyticsUsageStorageStorageUsageConnectionEdges.model_rebuild()
