from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import NetworkStatusDefault


class GetNetworkStatus(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    network_status: Optional["GetNetworkStatusNetworkStatus"] = Field(
        alias="networkStatus",
        description="Public network status: cluster topology, node counts, peer connections.\nNo tenant data exposed. Part of public allowlist.",
    )
    "Public network status: cluster topology, node counts, peer connections.\nNo tenant data exposed. Part of public allowlist."


GetNetworkStatusNetworkStatus = NetworkStatusDefault
GetNetworkStatus.model_rebuild()
