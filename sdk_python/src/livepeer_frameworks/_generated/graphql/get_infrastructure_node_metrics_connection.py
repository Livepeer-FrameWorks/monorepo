from typing import Annotated, Literal, Optional, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import NodeMetricDefault, PageInfoDefault


class GetInfrastructureNodeMetricsConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    node: Optional[
        Annotated[
            Union[
                "GetInfrastructureNodeMetricsConnectionNodeNode",
                "GetInfrastructureNodeMetricsConnectionNodeInfrastructureNode",
                UnknownMember,
            ],
            OpenUnion(),
        ]
    ] = Field(description="Fetch a single node by its global ID.")
    "Fetch a single node by its global ID."


class GetInfrastructureNodeMetricsConnectionNodeNode(BaseModel):
    """Relay-style global node interface.
    All fetchable objects implement this interface and can be retrieved by their global ID."""

    typename__: Literal[
        "APIUsageRecord",
        "ArtifactEvent",
        "BufferEvent",
        "ClientMetrics5m",
        "Clip",
        "Cluster",
        "ConnectionEvent",
        "Conversation",
        "Message",
        "Node",
        "NodeMetric",
        "NodeMetricHourly",
        "NodePerformance5m",
        "ProcessingUsageRecord",
        "QualityTierDaily",
        "SigningKey",
        "StorageEvent",
        "StorageUsageRecord",
        "Stream",
        "StreamAnalyticsDaily",
        "StreamConnectionHourly",
        "StreamEvent",
        "StreamHealth5m",
        "StreamHealthMetric",
        "TenantDailyStat",
        "TrackListEvent",
        "ViewerGeoHourly",
        "ViewerHoursHourly",
        "ViewerSession",
        "VodAsset",
    ] = Field(alias="__typename")


class GetInfrastructureNodeMetricsConnectionNodeInfrastructureNode(BaseModel):
    """Relay-style global node interface.
    All fetchable objects implement this interface and can be retrieved by their global ID."""

    typename__: Literal["InfrastructureNode"] = Field(alias="__typename")
    metrics_connection: "GetInfrastructureNodeMetricsConnectionNodeInfrastructureNodeMetricsConnection" = Field(
        alias="metricsConnection",
        description="Paginated time-series metrics for this node.",
    )
    "Paginated time-series metrics for this node."


class GetInfrastructureNodeMetricsConnectionNodeInfrastructureNodeMetricsConnection(
    BaseModel
):
    edges: list[
        "GetInfrastructureNodeMetricsConnectionNodeInfrastructureNodeMetricsConnectionEdges"
    ]
    page_info: "GetInfrastructureNodeMetricsConnectionNodeInfrastructureNodeMetricsConnectionPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


class GetInfrastructureNodeMetricsConnectionNodeInfrastructureNodeMetricsConnectionEdges(
    BaseModel
):
    cursor: str
    node: "GetInfrastructureNodeMetricsConnectionNodeInfrastructureNodeMetricsConnectionEdgesNode"


GetInfrastructureNodeMetricsConnectionNodeInfrastructureNodeMetricsConnectionEdgesNode = NodeMetricDefault
GetInfrastructureNodeMetricsConnectionNodeInfrastructureNodeMetricsConnectionPageInfo = PageInfoDefault
GetInfrastructureNodeMetricsConnection.model_rebuild()
GetInfrastructureNodeMetricsConnectionNodeInfrastructureNode.model_rebuild()
GetInfrastructureNodeMetricsConnectionNodeInfrastructureNodeMetricsConnection.model_rebuild()
GetInfrastructureNodeMetricsConnectionNodeInfrastructureNodeMetricsConnectionEdges.model_rebuild()
