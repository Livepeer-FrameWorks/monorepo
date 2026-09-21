from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import IngestEndpointFields


class ResolveIngestEndpoint(BaseModel):
    resolve_ingest_endpoint: Optional["ResolveIngestEndpointResolveIngestEndpoint"] = (
        Field(alias="resolveIngestEndpoint")
    )


class ResolveIngestEndpointResolveIngestEndpoint(BaseModel):
    primary: "ResolveIngestEndpointResolveIngestEndpointPrimary"
    fallbacks: list["ResolveIngestEndpointResolveIngestEndpointFallbacks"]
    metadata: Optional["ResolveIngestEndpointResolveIngestEndpointMetadata"]


ResolveIngestEndpointResolveIngestEndpointPrimary = IngestEndpointFields
ResolveIngestEndpointResolveIngestEndpointFallbacks = IngestEndpointFields


class ResolveIngestEndpointResolveIngestEndpointMetadata(BaseModel):
    stream_id: str = Field(alias="streamId")
    stream_key: str = Field(alias="streamKey")
    tenant_id: str = Field(alias="tenantId")
    recording_enabled: bool = Field(alias="recordingEnabled")


ResolveIngestEndpoint.model_rebuild()
ResolveIngestEndpointResolveIngestEndpoint.model_rebuild()
