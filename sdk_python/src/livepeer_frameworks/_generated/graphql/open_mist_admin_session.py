from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (
    AuthErrorDefault,
    MistAdminSessionDefault,
    NotFoundErrorDefault,
    ValidationErrorDefault,
)


class OpenMistAdminSession(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    open_mist_admin_session: Annotated[
        Union[
            "OpenMistAdminSessionOpenMistAdminSessionMistAdminSession",
            "OpenMistAdminSessionOpenMistAdminSessionValidationError",
            "OpenMistAdminSessionOpenMistAdminSessionNotFoundError",
            "OpenMistAdminSessionOpenMistAdminSessionAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="openMistAdminSession",
        description="Open the MistServer admin UI on a specific edge node. Returns a short-TTL\nsession token plus the per-edge POST URL the webapp must submit the token\nto (NOT a query parameter, so the token doesn't leak via referrers / URL\nhistory / access logs). On success the browser sets a `fw_mist_admin`\ncookie scoped to /_mist and is redirected to /_mist/.\n\nAuthority is infrastructure ownership, not subscriber access: Mist admin\naccess is effectively shell on the edge box, so only owner/admin users\nin the cluster owner tenant can open it. Holders of the platform_operator\ngrant are allowed as break-glass. Anything else returns AuthError without\nexposing whether the node exists.",
    )
    "Open the MistServer admin UI on a specific edge node. Returns a short-TTL\nsession token plus the per-edge POST URL the webapp must submit the token\nto (NOT a query parameter, so the token doesn't leak via referrers / URL\nhistory / access logs). On success the browser sets a `fw_mist_admin`\ncookie scoped to /_mist and is redirected to /_mist/.\n\nAuthority is infrastructure ownership, not subscriber access: Mist admin\naccess is effectively shell on the edge box, so only owner/admin users\nin the cluster owner tenant can open it. Holders of the platform_operator\ngrant are allowed as break-glass. Anything else returns AuthError without\nexposing whether the node exists."


class OpenMistAdminSessionOpenMistAdminSessionMistAdminSession(MistAdminSessionDefault):
    typename__: Literal["MistAdminSession"] = Field(alias="__typename")


class OpenMistAdminSessionOpenMistAdminSessionValidationError(ValidationErrorDefault):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class OpenMistAdminSessionOpenMistAdminSessionNotFoundError(NotFoundErrorDefault):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class OpenMistAdminSessionOpenMistAdminSessionAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


OpenMistAdminSession.model_rebuild()
