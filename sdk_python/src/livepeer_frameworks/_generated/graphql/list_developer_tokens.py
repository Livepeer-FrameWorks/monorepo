from pydantic import Field

from .base_model import BaseModel
from .fragments import DeveloperToken, PageInfo


class ListDeveloperTokens(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    developer_tokens_connection: "ListDeveloperTokensDeveloperTokensConnection" = Field(
        alias="developerTokensConnection",
        description="List API tokens for programmatic access.\nUsed to authenticate requests to the Developer API.",
    )
    "List API tokens for programmatic access.\nUsed to authenticate requests to the Developer API."


class ListDeveloperTokensDeveloperTokensConnection(BaseModel):
    nodes: list["ListDeveloperTokensDeveloperTokensConnectionNodes"]
    page_info: "ListDeveloperTokensDeveloperTokensConnectionPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


class ListDeveloperTokensDeveloperTokensConnectionNodes(DeveloperToken):
    """An API token for programmatic access to the GraphQL API.
    Tokens have scoped permissions and optional expiration."""

    pass


ListDeveloperTokensDeveloperTokensConnectionPageInfo = PageInfo
ListDeveloperTokens.model_rebuild()
ListDeveloperTokensDeveloperTokensConnection.model_rebuild()
