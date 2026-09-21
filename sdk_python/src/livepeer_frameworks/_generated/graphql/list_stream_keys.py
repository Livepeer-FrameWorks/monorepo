from pydantic import Field

from .base_model import BaseModel
from .fragments import PageInfoFields, StreamKeyFields


class ListStreamKeys(BaseModel):
    stream_keys_connection: "ListStreamKeysStreamKeysConnection" = Field(
        alias="streamKeysConnection"
    )


class ListStreamKeysStreamKeysConnection(BaseModel):
    nodes: list["ListStreamKeysStreamKeysConnectionNodes"]
    page_info: "ListStreamKeysStreamKeysConnectionPageInfo" = Field(alias="pageInfo")
    total_count: int = Field(alias="totalCount")


ListStreamKeysStreamKeysConnectionNodes = StreamKeyFields
ListStreamKeysStreamKeysConnectionPageInfo = PageInfoFields
ListStreamKeys.model_rebuild()
ListStreamKeysStreamKeysConnection.model_rebuild()
