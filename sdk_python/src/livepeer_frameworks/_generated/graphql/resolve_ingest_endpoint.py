from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import IngestEndpoint


class ResolveIngestEndpoint(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    resolve_ingest_endpoint: Optional["ResolveIngestEndpointResolveIngestEndpoint"] = (
        Field(
            alias="resolveIngestEndpoint",
            description="Resolve a stream key to ingest endpoints for StreamCrafter.\nReturns node-specific advertised protocols. A requested protocol filters candidates\nbefore ranking; it is not permission to substitute a different protocol.",
        )
    )
    "Resolve a stream key to ingest endpoints for StreamCrafter.\nReturns node-specific advertised protocols. A requested protocol filters candidates\nbefore ranking; it is not permission to substitute a different protocol."


class ResolveIngestEndpointResolveIngestEndpoint(BaseModel):
    primary: "ResolveIngestEndpointResolveIngestEndpointPrimary"
    fallbacks: list["ResolveIngestEndpointResolveIngestEndpointFallbacks"]
    metadata: Optional["ResolveIngestEndpointResolveIngestEndpointMetadata"]


ResolveIngestEndpointResolveIngestEndpointPrimary = IngestEndpoint
ResolveIngestEndpointResolveIngestEndpointFallbacks = IngestEndpoint


class ResolveIngestEndpointResolveIngestEndpointMetadata(BaseModel):
    stream_id: str = Field(alias="streamId")
    stream_key: str = Field(alias="streamKey")
    tenant_id: str = Field(alias="tenantId")
    recording_enabled: bool = Field(alias="recordingEnabled")


ResolveIngestEndpoint.model_rebuild()
ResolveIngestEndpointResolveIngestEndpoint.model_rebuild()
