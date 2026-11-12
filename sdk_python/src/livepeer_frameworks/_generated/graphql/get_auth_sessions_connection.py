from pydantic import Field

from .base_model import BaseModel
from .fragments import AuthSessionDefault, PageInfoDefault


class GetAuthSessionsConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    auth_sessions_connection: "GetAuthSessionsConnectionAuthSessionsConnection" = Field(
        alias="authSessionsConnection",
        description="The signed-in user’s own interactive sessions, newest first. Supports first=1..100.",
    )
    "The signed-in user’s own interactive sessions, newest first. Supports first=1..100."


class GetAuthSessionsConnectionAuthSessionsConnection(BaseModel):
    edges: list["GetAuthSessionsConnectionAuthSessionsConnectionEdges"]
    page_info: "GetAuthSessionsConnectionAuthSessionsConnectionPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


class GetAuthSessionsConnectionAuthSessionsConnectionEdges(BaseModel):
    cursor: str
    node: "GetAuthSessionsConnectionAuthSessionsConnectionEdgesNode"


class GetAuthSessionsConnectionAuthSessionsConnectionEdgesNode(AuthSessionDefault):
    """One of the signed-in user's own interactive sessions."""

    pass


GetAuthSessionsConnectionAuthSessionsConnectionPageInfo = PageInfoDefault
GetAuthSessionsConnection.model_rebuild()
GetAuthSessionsConnectionAuthSessionsConnection.model_rebuild()
GetAuthSessionsConnectionAuthSessionsConnectionEdges.model_rebuild()
