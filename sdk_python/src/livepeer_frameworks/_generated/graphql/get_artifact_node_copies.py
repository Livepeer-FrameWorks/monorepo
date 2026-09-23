from pydantic import Field

from .base_model import BaseModel
from .fragments import (
    AssetNodeCopiesDefault,
    AssetNodeCopiesDefaultCopies,  # noqa: F401
)


class GetArtifactNodeCopies(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetArtifactNodeCopiesAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetArtifactNodeCopiesAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    health: "GetArtifactNodeCopiesAnalyticsHealth" = Field(
        description="Health analytics: stream quality, rebuffering, client QoE."
    )
    "Health analytics: stream quality, rebuffering, client QoE."


class GetArtifactNodeCopiesAnalyticsHealth(BaseModel):
    """Health analytics for streams.
    `streamId` arguments accept Stream.id (Relay global ID)."""

    artifact_node_copies: "GetArtifactNodeCopiesAnalyticsHealthArtifactNodeCopies" = Field(
        alias="artifactNodeCopies",
        description="Nodes currently holding a transient LOCAL COPY of one artifact (not the durable\nobject-storage copy), from the node-copy telemetry the media plane emits. `role`\nis `origin` (producer/relay source) or `cache` (synced pull); `isComplete` marks a\nfull local copy. Read-through relay block caches are not represented. Node geo is\nenriched from the infrastructure registry. `artifactHash` is required. When\n`truncated` is true the node set was capped and is NOT exhaustive.",
    )
    "Nodes currently holding a transient LOCAL COPY of one artifact (not the durable\nobject-storage copy), from the node-copy telemetry the media plane emits. `role`\nis `origin` (producer/relay source) or `cache` (synced pull); `isComplete` marks a\nfull local copy. Read-through relay block caches are not represented. Node geo is\nenriched from the infrastructure registry. `artifactHash` is required. When\n`truncated` is true the node set was capped and is NOT exhaustive."


class GetArtifactNodeCopiesAnalyticsHealthArtifactNodeCopies(AssetNodeCopiesDefault):
    """A node-copy listing plus whether it was capped at the per-request limit. When
    `truncated` is true, `copies` is not exhaustive — present the count as a lower bound
    (e.g. "500+") or a "results truncated" note, never as an exact total."""

    pass


GetArtifactNodeCopies.model_rebuild()
GetArtifactNodeCopiesAnalytics.model_rebuild()
GetArtifactNodeCopiesAnalyticsHealth.model_rebuild()
