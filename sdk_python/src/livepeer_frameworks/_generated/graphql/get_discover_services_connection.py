from pydantic import Field

from .base_model import BaseModel
from .fragments import PageInfoDefault, ServiceInstanceDefault


class GetDiscoverServicesConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    discover_services_connection: "GetDiscoverServicesConnectionDiscoverServicesConnection" = Field(
        alias="discoverServicesConnection",
        description="Discover service instances by type.",
    )
    "Discover service instances by type."


class GetDiscoverServicesConnectionDiscoverServicesConnection(BaseModel):
    edges: list["GetDiscoverServicesConnectionDiscoverServicesConnectionEdges"]
    page_info: "GetDiscoverServicesConnectionDiscoverServicesConnectionPageInfo" = (
        Field(alias="pageInfo")
    )
    total_count: int = Field(alias="totalCount")


class GetDiscoverServicesConnectionDiscoverServicesConnectionEdges(BaseModel):
    cursor: str
    node: "GetDiscoverServicesConnectionDiscoverServicesConnectionEdgesNode"


GetDiscoverServicesConnectionDiscoverServicesConnectionEdgesNode = (
    ServiceInstanceDefault
)
GetDiscoverServicesConnectionDiscoverServicesConnectionPageInfo = PageInfoDefault
GetDiscoverServicesConnection.model_rebuild()
GetDiscoverServicesConnectionDiscoverServicesConnection.model_rebuild()
GetDiscoverServicesConnectionDiscoverServicesConnectionEdges.model_rebuild()
