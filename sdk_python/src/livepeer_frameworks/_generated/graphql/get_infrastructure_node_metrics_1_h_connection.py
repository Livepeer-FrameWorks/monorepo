from typing import Annotated, Literal, Optional, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import NodeMetricHourlyDefault, PageInfoDefault


class GetInfrastructureNodeMetrics1hConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    node: Optional[
        Annotated[
            Union[
                "GetInfrastructureNodeMetrics1hConnectionNodeNode",
                "GetInfrastructureNodeMetrics1hConnectionNodeInfrastructureNode",
                UnknownMember,
            ],
            OpenUnion(),
        ]
    ] = Field(description="Fetch a single node by its global ID.")
    "Fetch a single node by its global ID."


class GetInfrastructureNodeMetrics1hConnectionNodeNode(BaseModel):
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


class GetInfrastructureNodeMetrics1hConnectionNodeInfrastructureNode(BaseModel):
    """Relay-style global node interface.
    All fetchable objects implement this interface and can be retrieved by their global ID."""

    typename__: Literal["InfrastructureNode"] = Field(alias="__typename")
    metrics_1_h_connection: "GetInfrastructureNodeMetrics1hConnectionNodeInfrastructureNodeMetrics1HConnection" = Field(
        alias="metrics1hConnection",
        description="Hourly aggregated metrics for this node.",
    )
    "Hourly aggregated metrics for this node."


class GetInfrastructureNodeMetrics1hConnectionNodeInfrastructureNodeMetrics1HConnection(
    BaseModel
):
    edges: list[
        "GetInfrastructureNodeMetrics1hConnectionNodeInfrastructureNodeMetrics1HConnectionEdges"
    ]
    page_info: "GetInfrastructureNodeMetrics1hConnectionNodeInfrastructureNodeMetrics1HConnectionPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


class GetInfrastructureNodeMetrics1hConnectionNodeInfrastructureNodeMetrics1HConnectionEdges(
    BaseModel
):
    cursor: str
    node: "GetInfrastructureNodeMetrics1hConnectionNodeInfrastructureNodeMetrics1HConnectionEdgesNode"


GetInfrastructureNodeMetrics1hConnectionNodeInfrastructureNodeMetrics1HConnectionEdgesNode = NodeMetricHourlyDefault
GetInfrastructureNodeMetrics1hConnectionNodeInfrastructureNodeMetrics1HConnectionPageInfo = PageInfoDefault
GetInfrastructureNodeMetrics1hConnection.model_rebuild()
GetInfrastructureNodeMetrics1hConnectionNodeInfrastructureNode.model_rebuild()
GetInfrastructureNodeMetrics1hConnectionNodeInfrastructureNodeMetrics1HConnection.model_rebuild()
GetInfrastructureNodeMetrics1hConnectionNodeInfrastructureNodeMetrics1HConnectionEdges.model_rebuild()
