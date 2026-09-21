from pydantic import Field

from .base_model import BaseModel
from .fragments import PageInfo, StreamKey


class ListStreamKeys(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    stream_keys_connection: "ListStreamKeysStreamKeysConnection" = Field(
        alias="streamKeysConnection",
        description="List all stream keys for a specific stream.",
    )
    "List all stream keys for a specific stream."


class ListStreamKeysStreamKeysConnection(BaseModel):
    nodes: list["ListStreamKeysStreamKeysConnectionNodes"]
    page_info: "ListStreamKeysStreamKeysConnectionPageInfo" = Field(alias="pageInfo")
    total_count: int = Field(alias="totalCount")


ListStreamKeysStreamKeysConnectionNodes = StreamKey
ListStreamKeysStreamKeysConnectionPageInfo = PageInfo
ListStreamKeys.model_rebuild()
ListStreamKeysStreamKeysConnection.model_rebuild()
