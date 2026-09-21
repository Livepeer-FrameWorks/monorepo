from pydantic import Field

from .base_model import BaseModel
from .fragments import StorageArtifact


class ListArtifacts(BaseModel):
    storage_artifacts_connection: "ListArtifactsStorageArtifactsConnection" = Field(
        alias="storageArtifactsConnection"
    )


class ListArtifactsStorageArtifactsConnection(BaseModel):
    nodes: list["ListArtifactsStorageArtifactsConnectionNodes"]
    total_count: int = Field(alias="totalCount")
    has_next_page: bool = Field(alias="hasNextPage")
    limit: int
    offset: int


ListArtifactsStorageArtifactsConnectionNodes = StorageArtifact
ListArtifacts.model_rebuild()
ListArtifactsStorageArtifactsConnection.model_rebuild()
