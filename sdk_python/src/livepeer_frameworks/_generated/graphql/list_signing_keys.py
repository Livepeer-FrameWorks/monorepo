from pydantic import Field

from .base_model import BaseModel
from .fragments import PageInfo, SigningKey


class ListSigningKeys(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    signing_keys_connection: "ListSigningKeysSigningKeysConnection" = Field(
        alias="signingKeysConnection",
        description="List the tenant's playback signing keys with optional status filter.",
    )
    "List the tenant's playback signing keys with optional status filter."


class ListSigningKeysSigningKeysConnection(BaseModel):
    nodes: list["ListSigningKeysSigningKeysConnectionNodes"]
    page_info: "ListSigningKeysSigningKeysConnectionPageInfo" = Field(alias="pageInfo")
    total_count: int = Field(alias="totalCount")


class ListSigningKeysSigningKeysConnectionNodes(SigningKey):
    """A customer-managed signing key for issuing viewer playback JWTs. The private
    key is returned exactly once at creation time (in CreateSigningKeySuccess);
    FrameWorks stores only the public key. Up to 10 active keys per tenant."""

    pass


ListSigningKeysSigningKeysConnectionPageInfo = PageInfo
ListSigningKeys.model_rebuild()
ListSigningKeysSigningKeysConnection.model_rebuild()
