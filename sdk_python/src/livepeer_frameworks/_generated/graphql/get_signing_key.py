from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import SigningKey


class GetSigningKey(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    signing_key: Optional["GetSigningKeySigningKey"] = Field(
        alias="signingKey",
        description="Get a single playback signing key by ID. Tenant-scoped.",
    )
    "Get a single playback signing key by ID. Tenant-scoped."


class GetSigningKeySigningKey(SigningKey):
    """A customer-managed signing key for issuing viewer playback JWTs. The private
    key is returned exactly once at creation time (in CreateSigningKeySuccess);
    FrameWorks stores only the public key. Up to 10 active keys per tenant."""

    pass


GetSigningKey.model_rebuild()
