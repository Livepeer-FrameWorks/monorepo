from pydantic import Field

from .base_model import BaseModel
from .fragments import OrchestratorPerformancePointDefault


class GetOrchestratorPerformanceSeries(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    orchestrator_performance_series: list[
        "GetOrchestratorPerformanceSeriesOrchestratorPerformanceSeries"
    ] = Field(
        alias="orchestratorPerformanceSeries",
        description="Time-series performance points for an orchestrator in 5m or 1h buckets per\ngateway, region and instance IP: gateway discovery and transcode attempts\nfrom both the legacy rollups and the network observations the public route\nserves (raw, 30 days), counted as that route counts them, plus legacy AI\noutcomes. `meanLatencyMs` is server-pre-computed; callers don't divide\nlatency_sum/latency_count themselves.",
    )
    "Time-series performance points for an orchestrator in 5m or 1h buckets per\ngateway, region and instance IP: gateway discovery and transcode attempts\nfrom both the legacy rollups and the network observations the public route\nserves (raw, 30 days), counted as that route counts them, plus legacy AI\noutcomes. `meanLatencyMs` is server-pre-computed; callers don't divide\nlatency_sum/latency_count themselves."


class GetOrchestratorPerformanceSeriesOrchestratorPerformanceSeries(
    OrchestratorPerformancePointDefault
):
    """One sample from discovery and outcome rollups. Discovery metrics are
    per-vantage; transcode and AI outcome metrics are keyed by the gateway and the
    resolved instance IP observed by the gateway."""

    pass


GetOrchestratorPerformanceSeries.model_rebuild()
