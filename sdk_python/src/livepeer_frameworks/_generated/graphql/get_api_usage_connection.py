from datetime import datetime

from pydantic import Field

from .base_model import BaseModel
from .fragments import APIUsageRecordDefault, PageInfoDefault


class GetApiUsageConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetApiUsageConnectionAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetApiUsageConnectionAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    usage: "GetApiUsageConnectionAnalyticsUsage" = Field(
        description="Usage analytics: streaming hours, storage, and processing."
    )
    "Usage analytics: streaming hours, storage, and processing."


class GetApiUsageConnectionAnalyticsUsage(BaseModel):
    """Usage analytics grouped by type."""

    api: "GetApiUsageConnectionAnalyticsUsageApi" = Field(
        description="API usage: GraphQL request counts, auth type breakdown, operation analytics."
    )
    "API usage: GraphQL request counts, auth type breakdown, operation analytics."


class GetApiUsageConnectionAnalyticsUsageApi(BaseModel):
    """API usage analytics.
    Tracks GraphQL API request counts by auth type, operation type, and operation name."""

    api_usage_connection: "GetApiUsageConnectionAnalyticsUsageApiApiUsageConnection" = (
        Field(alias="apiUsageConnection")
    )


class GetApiUsageConnectionAnalyticsUsageApiApiUsageConnection(BaseModel):
    edges: list["GetApiUsageConnectionAnalyticsUsageApiApiUsageConnectionEdges"]
    page_info: "GetApiUsageConnectionAnalyticsUsageApiApiUsageConnectionPageInfo" = (
        Field(alias="pageInfo")
    )
    total_count: int = Field(alias="totalCount")
    summaries: list["GetApiUsageConnectionAnalyticsUsageApiApiUsageConnectionSummaries"]
    operation_summaries: list[
        "GetApiUsageConnectionAnalyticsUsageApiApiUsageConnectionOperationSummaries"
    ] = Field(alias="operationSummaries")


class GetApiUsageConnectionAnalyticsUsageApiApiUsageConnectionEdges(BaseModel):
    cursor: str
    node: "GetApiUsageConnectionAnalyticsUsageApiApiUsageConnectionEdgesNode"


GetApiUsageConnectionAnalyticsUsageApiApiUsageConnectionEdgesNode = (
    APIUsageRecordDefault
)
GetApiUsageConnectionAnalyticsUsageApiApiUsageConnectionPageInfo = PageInfoDefault


class GetApiUsageConnectionAnalyticsUsageApiApiUsageConnectionSummaries(BaseModel):
    date: datetime
    auth_type: str = Field(alias="authType")
    total_requests: int = Field(alias="totalRequests")
    total_errors: int = Field(alias="totalErrors")
    avg_duration_ms: float = Field(alias="avgDurationMs")
    total_complexity: int = Field(alias="totalComplexity")
    unique_users: int = Field(alias="uniqueUsers")
    unique_tokens: int = Field(alias="uniqueTokens")


class GetApiUsageConnectionAnalyticsUsageApiApiUsageConnectionOperationSummaries(
    BaseModel
):
    operation_type: str = Field(alias="operationType")
    total_requests: int = Field(alias="totalRequests")
    total_errors: int = Field(alias="totalErrors")
    unique_operations: int = Field(alias="uniqueOperations")
    avg_duration_ms: float = Field(alias="avgDurationMs")
    total_complexity: int = Field(alias="totalComplexity")


GetApiUsageConnection.model_rebuild()
GetApiUsageConnectionAnalytics.model_rebuild()
GetApiUsageConnectionAnalyticsUsage.model_rebuild()
GetApiUsageConnectionAnalyticsUsageApi.model_rebuild()
GetApiUsageConnectionAnalyticsUsageApiApiUsageConnection.model_rebuild()
GetApiUsageConnectionAnalyticsUsageApiApiUsageConnectionEdges.model_rebuild()
