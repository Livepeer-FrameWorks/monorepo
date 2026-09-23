from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import AuthErrorDefault, LinkEmailPayloadDefault, ValidationErrorDefault


class LinkEmail(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    link_email: Annotated[
        Union[
            "LinkEmailLinkEmailLinkEmailPayload",
            "LinkEmailLinkEmailValidationError",
            "LinkEmailLinkEmailAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="linkEmail",
        description="Link an email to a wallet-only account.\nThis enables the upgrade path from prepaid to postpaid billing.\nA verification email will be sent to confirm the address.",
    )
    "Link an email to a wallet-only account.\nThis enables the upgrade path from prepaid to postpaid billing.\nA verification email will be sent to confirm the address."


class LinkEmailLinkEmailLinkEmailPayload(LinkEmailPayloadDefault):
    typename__: Literal["LinkEmailPayload"] = Field(alias="__typename")


class LinkEmailLinkEmailValidationError(ValidationErrorDefault):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class LinkEmailLinkEmailAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


LinkEmail.model_rebuild()
