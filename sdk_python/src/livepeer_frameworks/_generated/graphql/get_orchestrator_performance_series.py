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
        description="Time-series performance points for an orchestrator from the discovery\nrollups (5m or 1h). `meanLatencyMs` is server-pre-computed; callers don't\ndivide latency_sum/latency_count themselves.",
    )
    "Time-series performance points for an orchestrator from the discovery\nrollups (5m or 1h). `meanLatencyMs` is server-pre-computed; callers don't\ndivide latency_sum/latency_count themselves."


class GetOrchestratorPerformanceSeriesOrchestratorPerformanceSeries(
    OrchestratorPerformancePointDefault
):
    """One sample from discovery and outcome rollups. Discovery metrics are
    per-vantage; transcode and AI outcome metrics are keyed by the gateway and the
    resolved instance IP observed by the gateway."""

    pass


GetOrchestratorPerformanceSeries.model_rebuild()
