from pydantic import Field

from .base_model import BaseModel
from .fragments import PageInfoDefault, SecurityActivityEventDefault


class GetSecurityActivityConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    security_activity_connection: "GetSecurityActivityConnectionSecurityActivityConnection" = Field(
        alias="securityActivityConnection",
        description="Your own security activity from the past 90 days.",
    )
    "Your own security activity from the past 90 days."


class GetSecurityActivityConnectionSecurityActivityConnection(BaseModel):
    edges: list["GetSecurityActivityConnectionSecurityActivityConnectionEdges"]
    page_info: "GetSecurityActivityConnectionSecurityActivityConnectionPageInfo" = (
        Field(alias="pageInfo")
    )
    total_count: int = Field(alias="totalCount")


class GetSecurityActivityConnectionSecurityActivityConnectionEdges(BaseModel):
    cursor: str
    node: "GetSecurityActivityConnectionSecurityActivityConnectionEdgesNode"


class GetSecurityActivityConnectionSecurityActivityConnectionEdgesNode(
    SecurityActivityEventDefault
):
    """A security event concerning only the signed-in user."""

    pass


GetSecurityActivityConnectionSecurityActivityConnectionPageInfo = PageInfoDefault
GetSecurityActivityConnection.model_rebuild()
GetSecurityActivityConnectionSecurityActivityConnection.model_rebuild()
GetSecurityActivityConnectionSecurityActivityConnectionEdges.model_rebuild()
