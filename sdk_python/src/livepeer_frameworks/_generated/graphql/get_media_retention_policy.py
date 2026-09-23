from pydantic import Field

from .base_model import BaseModel
from .fragments import (
    MediaRetentionPolicyDefault,
    MediaRetentionPolicyDefaultBounds,  # noqa: F401
)


class GetMediaRetentionPolicy(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    media_retention_policy: "GetMediaRetentionPolicyMediaRetentionPolicy" = Field(
        alias="mediaRetentionPolicy",
        description='Tenant-default retention policy + entitlement bounds + the value the\ncascade (per-asset override → tenant default → tier entitlement) would\nresolve to today. Used by the webapp\'s "Storage retention defaults"\npanel to size sliders and explain the allowed range.',
    )
    'Tenant-default retention policy + entitlement bounds + the value the\ncascade (per-asset override → tenant default → tier entitlement) would\nresolve to today. Used by the webapp\'s "Storage retention defaults"\npanel to size sliders and explain the allowed range.'


class GetMediaRetentionPolicyMediaRetentionPolicy(MediaRetentionPolicyDefault):
    """Tenant-default retention policy: per-class overrides + the values the
    cascade would resolve to today for a hypothetical new artifact of each
    class (no per-stream context)."""

    pass


GetMediaRetentionPolicy.model_rebuild()
