from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import PageInfo, StreamMetrics


class ListStreamMetrics(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    streams_connection: "ListStreamMetricsStreamsConnection" = Field(
        alias="streamsConnection",
        description="List all streams for the current tenant with pagination.\nAn API token needs the streams:read or streams:write scope. Stream.streamKey\nneeds streams:write.",
    )
    "List all streams for the current tenant with pagination.\nAn API token needs the streams:read or streams:write scope. Stream.streamKey\nneeds streams:write."


class ListStreamMetricsStreamsConnection(BaseModel):
    nodes: list["ListStreamMetricsStreamsConnectionNodes"]
    page_info: "ListStreamMetricsStreamsConnectionPageInfo" = Field(alias="pageInfo")
    total_count: int = Field(alias="totalCount")


class ListStreamMetricsStreamsConnectionNodes(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

    id: str = Field(description="Global unique identifier for Relay compatibility.")
    "Global unique identifier for Relay compatibility."
    stream_id: str = Field(
        alias="streamId",
        description="Public stream UUID used for analytics and service APIs (not the Relay ID).",
    )
    "Public stream UUID used for analytics and service APIs (not the Relay ID)."
    metrics: Optional["ListStreamMetricsStreamsConnectionNodesMetrics"] = Field(
        description="Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."
    )
    "Real-time operational metrics from the data plane.\nIncludes viewer counts, quality metrics, and throughput data.\nLazily loaded from ClickHouse analytics, so an API token needs the\nanalytics:read scope: without it this field is null and the response\ncarries a FORBIDDEN error at its path."


class ListStreamMetricsStreamsConnectionNodesMetrics(StreamMetrics):
    """Real-time operational metrics for a stream from the analytics data plane.
    Updated frequently while stream is live, represents latest known state."""

    pass


ListStreamMetricsStreamsConnectionPageInfo = PageInfo
ListStreamMetrics.model_rebuild()
ListStreamMetricsStreamsConnection.model_rebuild()
ListStreamMetricsStreamsConnectionNodes.model_rebuild()
