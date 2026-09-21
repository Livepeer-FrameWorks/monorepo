from pydantic import Field

from .base_model import BaseModel
from .fragments import PageInfo, Stream


class ListStreams(BaseModel):
    streams_connection: "ListStreamsStreamsConnection" = Field(
        alias="streamsConnection"
    )


class ListStreamsStreamsConnection(BaseModel):
    nodes: list["ListStreamsStreamsConnectionNodes"]
    page_info: "ListStreamsStreamsConnectionPageInfo" = Field(alias="pageInfo")
    total_count: int = Field(alias="totalCount")


ListStreamsStreamsConnectionNodes = Stream
ListStreamsStreamsConnectionPageInfo = PageInfo
ListStreams.model_rebuild()
ListStreamsStreamsConnection.model_rebuild()
