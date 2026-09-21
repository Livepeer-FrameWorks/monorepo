from pydantic import Field

from .base_model import BaseModel
from .fragments import DeveloperTokenFields, PageInfoFields


class ListDeveloperTokens(BaseModel):
    developer_tokens_connection: "ListDeveloperTokensDeveloperTokensConnection" = Field(
        alias="developerTokensConnection"
    )


class ListDeveloperTokensDeveloperTokensConnection(BaseModel):
    nodes: list["ListDeveloperTokensDeveloperTokensConnectionNodes"]
    page_info: "ListDeveloperTokensDeveloperTokensConnectionPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


ListDeveloperTokensDeveloperTokensConnectionNodes = DeveloperTokenFields
ListDeveloperTokensDeveloperTokensConnectionPageInfo = PageInfoFields
ListDeveloperTokens.model_rebuild()
ListDeveloperTokensDeveloperTokensConnection.model_rebuild()
