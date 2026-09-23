from datetime import datetime

from pydantic import Field

from .base_model import BaseModel
from .fragments import PageInfoDefault, ProcessingUsageRecordDefault


class GetProcessingUsageConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetProcessingUsageConnectionAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetProcessingUsageConnectionAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    usage: "GetProcessingUsageConnectionAnalyticsUsage" = Field(
        description="Usage analytics: streaming hours, storage, and processing."
    )
    "Usage analytics: streaming hours, storage, and processing."


class GetProcessingUsageConnectionAnalyticsUsage(BaseModel):
    """Usage analytics grouped by type."""

    processing: "GetProcessingUsageConnectionAnalyticsUsageProcessing" = Field(
        description="Processing usage: transcoding, clipping, DVR operations."
    )
    "Processing usage: transcoding, clipping, DVR operations."


class GetProcessingUsageConnectionAnalyticsUsageProcessing(BaseModel):
    """Processing usage analytics.
    `streamId` arguments accept Stream.id (Relay global ID)."""

    processing_usage_connection: "GetProcessingUsageConnectionAnalyticsUsageProcessingProcessingUsageConnection" = Field(
        alias="processingUsageConnection"
    )


class GetProcessingUsageConnectionAnalyticsUsageProcessingProcessingUsageConnection(
    BaseModel
):
    edges: list[
        "GetProcessingUsageConnectionAnalyticsUsageProcessingProcessingUsageConnectionEdges"
    ]
    page_info: "GetProcessingUsageConnectionAnalyticsUsageProcessingProcessingUsageConnectionPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")
    summaries: list[
        "GetProcessingUsageConnectionAnalyticsUsageProcessingProcessingUsageConnectionSummaries"
    ]


class GetProcessingUsageConnectionAnalyticsUsageProcessingProcessingUsageConnectionEdges(
    BaseModel
):
    cursor: str
    node: "GetProcessingUsageConnectionAnalyticsUsageProcessingProcessingUsageConnectionEdgesNode"


GetProcessingUsageConnectionAnalyticsUsageProcessingProcessingUsageConnectionEdgesNode = ProcessingUsageRecordDefault
GetProcessingUsageConnectionAnalyticsUsageProcessingProcessingUsageConnectionPageInfo = PageInfoDefault


class GetProcessingUsageConnectionAnalyticsUsageProcessingProcessingUsageConnectionSummaries(
    BaseModel
):
    date: datetime
    livepeer_seconds: float = Field(alias="livepeerSeconds")
    livepeer_segment_count: int = Field(alias="livepeerSegmentCount")
    livepeer_unique_streams: int = Field(alias="livepeerUniqueStreams")
    livepeer_h_264_seconds: float = Field(alias="livepeerH264Seconds")
    livepeer_vp_9_seconds: float = Field(alias="livepeerVp9Seconds")
    livepeer_av_1_seconds: float = Field(alias="livepeerAv1Seconds")
    livepeer_hevc_seconds: float = Field(alias="livepeerHevcSeconds")
    native_av_seconds: float = Field(alias="nativeAvSeconds")
    native_av_segment_count: int = Field(alias="nativeAvSegmentCount")
    native_av_unique_streams: int = Field(alias="nativeAvUniqueStreams")
    native_av_h_264_seconds: float = Field(alias="nativeAvH264Seconds")
    native_av_vp_9_seconds: float = Field(alias="nativeAvVp9Seconds")
    native_av_av_1_seconds: float = Field(alias="nativeAvAv1Seconds")
    native_av_hevc_seconds: float = Field(alias="nativeAvHevcSeconds")
    native_av_aac_seconds: float = Field(alias="nativeAvAacSeconds")
    native_av_opus_seconds: float = Field(alias="nativeAvOpusSeconds")
    audio_seconds: float = Field(alias="audioSeconds")
    video_seconds: float = Field(alias="videoSeconds")


GetProcessingUsageConnection.model_rebuild()
GetProcessingUsageConnectionAnalytics.model_rebuild()
GetProcessingUsageConnectionAnalyticsUsage.model_rebuild()
GetProcessingUsageConnectionAnalyticsUsageProcessing.model_rebuild()
GetProcessingUsageConnectionAnalyticsUsageProcessingProcessingUsageConnection.model_rebuild()
GetProcessingUsageConnectionAnalyticsUsageProcessingProcessingUsageConnectionEdges.model_rebuild()
