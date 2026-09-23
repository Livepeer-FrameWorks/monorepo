from typing import Annotated, Literal, Optional, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (  # noqa: F401
    APIUsageRecordDefault,
    ArtifactEventInNodeDefault,
    ArtifactEventInNodeDefaultStream,
    BufferEventInNodeDefault,
    ClientMetrics5mDefault,
    ClientMetrics5mDefaultStream,
    ClipInNodeDefault,
    ClipInNodeDefaultEffectiveRetention,
    ClipInNodeDefaultPlaybackPolicy,
    ClipInNodeDefaultStorageCost,
    ClipInNodeDefaultStream,
    ClipInNodeDefaultThumbnailAssets,
    ClusterInNodeDefault,
    ConnectionEventInNodeDefault,
    ConnectionEventInNodeDefaultClientBucket,
    ConnectionEventInNodeDefaultNodeBucket,
    ConnectionEventInNodeDefaultStream,
    ConversationInNodeDefault,
    ConversationInNodeDefaultLastMessage,
    InfrastructureNodeInNodeDefault,
    InfrastructureNodeInNodeDefaultLiveState,
    InfrastructureNodeInNodeDefaultRoutingImpactPreview,
    MessageDefault,
    NodeMetricDefault,
    NodeMetricHourlyDefault,
    NodePerformance5mDefault,
    ProcessingUsageRecordInNodeDefault,
    ProcessingUsageRecordInNodeDefaultStream,
    QualityTierDailyDefault,
    QualityTierDailyDefaultStream,
    SigningKeyInNodeDefault,
    StorageEventInNodeDefault,
    StorageEventInNodeDefaultStream,
    StorageUsageRecordDefault,
    StreamAnalyticsDailyDefault,
    StreamAnalyticsDailyDefaultStream,
    StreamConnectionHourlyDefault,
    StreamConnectionHourlyDefaultStream,
    StreamEventInNodeDefault,
    StreamEventInNodeDefaultStream,
    StreamHealth5mInNodeDefault,
    StreamHealthMetricInNodeDefault,
    StreamHealthMetricInNodeDefaultStream,
    StreamInNodeDefault,
    StreamInNodeDefaultManagedSource,
    StreamInNodeDefaultMetrics,
    StreamInNodeDefaultPlaybackPolicy,
    StreamInNodeDefaultPullSource,
    StreamInNodeDefaultPushTargets,
    StreamInNodeDefaultRetentionOverrides,
    StreamInNodeDefaultSourceLocation,
    StreamInNodeDefaultThumbnailAssets,
    TenantDailyStatDefault,
    TrackListEventInNodeDefault,
    TrackListEventInNodeDefaultStream,
    TrackListEventInNodeDefaultTracks,
    ViewerGeoHourlyInNodeDefault,
    ViewerHoursHourlyInNodeDefault,
    ViewerHoursHourlyInNodeDefaultStream,
    ViewerSessionInNodeDefault,
    ViewerSessionInNodeDefaultClientBucket,
    ViewerSessionInNodeDefaultStream,
    VodAssetInNodeDefault,
    VodAssetInNodeDefaultEffectiveRetention,
    VodAssetInNodeDefaultPlaybackPolicy,
    VodAssetInNodeDefaultStorageCost,
    VodAssetInNodeDefaultThumbnailAssets,
)


class GetNode(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    node: Optional[
        Annotated[
            Union[
                "GetNodeNodeNode",
                "GetNodeNodeAPIUsageRecord",
                "GetNodeNodeArtifactEvent",
                "GetNodeNodeBufferEvent",
                "GetNodeNodeClientMetrics5m",
                "GetNodeNodeClip",
                "GetNodeNodeCluster",
                "GetNodeNodeConnectionEvent",
                "GetNodeNodeConversation",
                "GetNodeNodeInfrastructureNode",
                "GetNodeNodeMessage",
                "GetNodeNodeNodeMetric",
                "GetNodeNodeNodeMetricHourly",
                "GetNodeNodeNodePerformance5m",
                "GetNodeNodeProcessingUsageRecord",
                "GetNodeNodeQualityTierDaily",
                "GetNodeNodeSigningKey",
                "GetNodeNodeStorageEvent",
                "GetNodeNodeStorageUsageRecord",
                "GetNodeNodeStream",
                "GetNodeNodeStreamAnalyticsDaily",
                "GetNodeNodeStreamConnectionHourly",
                "GetNodeNodeStreamEvent",
                "GetNodeNodeStreamHealth5m",
                "GetNodeNodeStreamHealthMetric",
                "GetNodeNodeTenantDailyStat",
                "GetNodeNodeTrackListEvent",
                "GetNodeNodeViewerGeoHourly",
                "GetNodeNodeViewerHoursHourly",
                "GetNodeNodeViewerSession",
                "GetNodeNodeVodAsset",
                UnknownMember,
            ],
            OpenUnion(),
        ]
    ] = Field(description="Fetch a single node by its global ID.")
    "Fetch a single node by its global ID."


class GetNodeNodeNode(BaseModel):
    """Relay-style global node interface.
    All fetchable objects implement this interface and can be retrieved by their global ID."""

    typename__: Literal["Node"] = Field(alias="__typename")


class GetNodeNodeAPIUsageRecord(APIUsageRecordDefault):
    """Relay-style global node interface.
    All fetchable objects implement this interface and can be retrieved by their global ID."""

    typename__: Literal["APIUsageRecord"] = Field(alias="__typename")


class GetNodeNodeArtifactEvent(ArtifactEventInNodeDefault):
    """Relay-style global node interface.
    All fetchable objects implement this interface and can be retrieved by their global ID."""

    typename__: Literal["ArtifactEvent"] = Field(alias="__typename")


class GetNodeNodeBufferEvent(BufferEventInNodeDefault):
    """Relay-style global node interface.
    All fetchable objects implement this interface and can be retrieved by their global ID."""

    typename__: Literal["BufferEvent"] = Field(alias="__typename")


class GetNodeNodeClientMetrics5m(ClientMetrics5mDefault):
    """Relay-style global node interface.
    All fetchable objects implement this interface and can be retrieved by their global ID."""

    typename__: Literal["ClientMetrics5m"] = Field(alias="__typename")


class GetNodeNodeClip(ClipInNodeDefault):
    """Relay-style global node interface.
    All fetchable objects implement this interface and can be retrieved by their global ID."""

    typename__: Literal["Clip"] = Field(alias="__typename")


class GetNodeNodeCluster(ClusterInNodeDefault):
    """Relay-style global node interface.
    All fetchable objects implement this interface and can be retrieved by their global ID."""

    typename__: Literal["Cluster"] = Field(alias="__typename")


class GetNodeNodeConnectionEvent(ConnectionEventInNodeDefault):
    """Relay-style global node interface.
    All fetchable objects implement this interface and can be retrieved by their global ID."""

    typename__: Literal["ConnectionEvent"] = Field(alias="__typename")


class GetNodeNodeConversation(ConversationInNodeDefault):
    """Relay-style global node interface.
    All fetchable objects implement this interface and can be retrieved by their global ID."""

    typename__: Literal["Conversation"] = Field(alias="__typename")


class GetNodeNodeInfrastructureNode(InfrastructureNodeInNodeDefault):
    """Relay-style global node interface.
    All fetchable objects implement this interface and can be retrieved by their global ID."""

    typename__: Literal["InfrastructureNode"] = Field(alias="__typename")


class GetNodeNodeMessage(MessageDefault):
    """Relay-style global node interface.
    All fetchable objects implement this interface and can be retrieved by their global ID."""

    typename__: Literal["Message"] = Field(alias="__typename")


class GetNodeNodeNodeMetric(NodeMetricDefault):
    """Relay-style global node interface.
    All fetchable objects implement this interface and can be retrieved by their global ID."""

    typename__: Literal["NodeMetric"] = Field(alias="__typename")


class GetNodeNodeNodeMetricHourly(NodeMetricHourlyDefault):
    """Relay-style global node interface.
    All fetchable objects implement this interface and can be retrieved by their global ID."""

    typename__: Literal["NodeMetricHourly"] = Field(alias="__typename")


class GetNodeNodeNodePerformance5m(NodePerformance5mDefault):
    """Relay-style global node interface.
    All fetchable objects implement this interface and can be retrieved by their global ID."""

    typename__: Literal["NodePerformance5m"] = Field(alias="__typename")


class GetNodeNodeProcessingUsageRecord(ProcessingUsageRecordInNodeDefault):
    """Relay-style global node interface.
    All fetchable objects implement this interface and can be retrieved by their global ID."""

    typename__: Literal["ProcessingUsageRecord"] = Field(alias="__typename")


class GetNodeNodeQualityTierDaily(QualityTierDailyDefault):
    """Relay-style global node interface.
    All fetchable objects implement this interface and can be retrieved by their global ID."""

    typename__: Literal["QualityTierDaily"] = Field(alias="__typename")


class GetNodeNodeSigningKey(SigningKeyInNodeDefault):
    """Relay-style global node interface.
    All fetchable objects implement this interface and can be retrieved by their global ID."""

    typename__: Literal["SigningKey"] = Field(alias="__typename")


class GetNodeNodeStorageEvent(StorageEventInNodeDefault):
    """Relay-style global node interface.
    All fetchable objects implement this interface and can be retrieved by their global ID."""

    typename__: Literal["StorageEvent"] = Field(alias="__typename")


class GetNodeNodeStorageUsageRecord(StorageUsageRecordDefault):
    """Relay-style global node interface.
    All fetchable objects implement this interface and can be retrieved by their global ID."""

    typename__: Literal["StorageUsageRecord"] = Field(alias="__typename")


class GetNodeNodeStream(StreamInNodeDefault):
    """Relay-style global node interface.
    All fetchable objects implement this interface and can be retrieved by their global ID."""

    typename__: Literal["Stream"] = Field(alias="__typename")


class GetNodeNodeStreamAnalyticsDaily(StreamAnalyticsDailyDefault):
    """Relay-style global node interface.
    All fetchable objects implement this interface and can be retrieved by their global ID."""

    typename__: Literal["StreamAnalyticsDaily"] = Field(alias="__typename")


class GetNodeNodeStreamConnectionHourly(StreamConnectionHourlyDefault):
    """Relay-style global node interface.
    All fetchable objects implement this interface and can be retrieved by their global ID."""

    typename__: Literal["StreamConnectionHourly"] = Field(alias="__typename")


class GetNodeNodeStreamEvent(StreamEventInNodeDefault):
    """Relay-style global node interface.
    All fetchable objects implement this interface and can be retrieved by their global ID."""

    typename__: Literal["StreamEvent"] = Field(alias="__typename")


class GetNodeNodeStreamHealth5m(StreamHealth5mInNodeDefault):
    """Relay-style global node interface.
    All fetchable objects implement this interface and can be retrieved by their global ID."""

    typename__: Literal["StreamHealth5m"] = Field(alias="__typename")


class GetNodeNodeStreamHealthMetric(StreamHealthMetricInNodeDefault):
    """Relay-style global node interface.
    All fetchable objects implement this interface and can be retrieved by their global ID."""

    typename__: Literal["StreamHealthMetric"] = Field(alias="__typename")


class GetNodeNodeTenantDailyStat(TenantDailyStatDefault):
    """Relay-style global node interface.
    All fetchable objects implement this interface and can be retrieved by their global ID."""

    typename__: Literal["TenantDailyStat"] = Field(alias="__typename")


class GetNodeNodeTrackListEvent(TrackListEventInNodeDefault):
    """Relay-style global node interface.
    All fetchable objects implement this interface and can be retrieved by their global ID."""

    typename__: Literal["TrackListEvent"] = Field(alias="__typename")


class GetNodeNodeViewerGeoHourly(ViewerGeoHourlyInNodeDefault):
    """Relay-style global node interface.
    All fetchable objects implement this interface and can be retrieved by their global ID."""

    typename__: Literal["ViewerGeoHourly"] = Field(alias="__typename")


class GetNodeNodeViewerHoursHourly(ViewerHoursHourlyInNodeDefault):
    """Relay-style global node interface.
    All fetchable objects implement this interface and can be retrieved by their global ID."""

    typename__: Literal["ViewerHoursHourly"] = Field(alias="__typename")


class GetNodeNodeViewerSession(ViewerSessionInNodeDefault):
    """Relay-style global node interface.
    All fetchable objects implement this interface and can be retrieved by their global ID."""

    typename__: Literal["ViewerSession"] = Field(alias="__typename")


class GetNodeNodeVodAsset(VodAssetInNodeDefault):
    """Relay-style global node interface.
    All fetchable objects implement this interface and can be retrieved by their global ID."""

    typename__: Literal["VodAsset"] = Field(alias="__typename")


GetNode.model_rebuild()
