from pydantic import Field

from .base_model import BaseModel
from .fragments import StorageArtifact


class ListArtifacts(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    storage_artifacts_connection: "ListArtifactsStorageArtifactsConnection" = Field(
        alias="storageArtifactsConnection",
        description="Unified storage artifact browser for the account Storage page.\nSearch, kind filters, stream scoping, sorting, and pagination are\nresolved server-side against the tenant artifact registry.",
    )
    "Unified storage artifact browser for the account Storage page.\nSearch, kind filters, stream scoping, sorting, and pagination are\nresolved server-side against the tenant artifact registry."


class ListArtifactsStorageArtifactsConnection(BaseModel):
    nodes: list["ListArtifactsStorageArtifactsConnectionNodes"]
    total_count: int = Field(alias="totalCount")
    has_next_page: bool = Field(alias="hasNextPage")
    limit: int
    offset: int


ListArtifactsStorageArtifactsConnectionNodes = StorageArtifact
ListArtifacts.model_rebuild()
ListArtifactsStorageArtifactsConnection.model_rebuild()
