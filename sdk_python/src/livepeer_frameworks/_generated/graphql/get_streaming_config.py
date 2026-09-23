from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import StreamingConfigDefault


class GetStreamingConfig(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    streaming_config: Optional["GetStreamingConfigStreamingConfig"] = Field(
        alias="streamingConfig",
        description="Cluster-aware streaming configuration for the authenticated tenant.\nReturns preferred and official cluster domains for building protocol-specific URLs.\nNull when not authenticated or cluster routing unavailable — frontend falls back to env vars.",
    )
    "Cluster-aware streaming configuration for the authenticated tenant.\nReturns preferred and official cluster domains for building protocol-specific URLs.\nNull when not authenticated or cluster routing unavailable — frontend falls back to env vars."


GetStreamingConfigStreamingConfig = StreamingConfigDefault
GetStreamingConfig.model_rebuild()
