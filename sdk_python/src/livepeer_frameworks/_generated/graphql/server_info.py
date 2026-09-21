from pydantic import Field

from .base_model import BaseModel


class ServerInfo(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    server_info: "ServerInfoServerInfo" = Field(
        alias="serverInfo",
        description="Platform release and shipped product features. Readable without\nauthentication so clients can detect what this server supports before\nsigning in.",
    )
    "Platform release and shipped product features. Readable without\nauthentication so clients can detect what this server supports before\nsigning in."


class ServerInfoServerInfo(BaseModel):
    """Release and shipped feature set of this server."""

    version: str = Field(description="Platform release version, for example v0.3.11.")
    "Platform release version, for example v0.3.11."
    features: list[str] = Field(
        description="Sorted slugs of shipped product features in the platform feature registry."
    )
    "Sorted slugs of shipped product features in the platform feature registry."


ServerInfo.model_rebuild()
