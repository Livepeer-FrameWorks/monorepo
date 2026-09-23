from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (
    AuthErrorDefault,
    PromoteToPaidPayloadDefault,
    ValidationErrorDefault,
)


class PromoteToPaid(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    promote_to_paid: Annotated[
        Union[
            "PromoteToPaidPromoteToPaidPromoteToPaidPayload",
            "PromoteToPaidPromoteToPaidValidationError",
            "PromoteToPaidPromoteToPaidAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="promoteToPaid",
        description="Switch from prepaid to a selected postpaid tier.\nA verified email is always required. Free needs no billing profile or payment\nprovider; paid tiers require confirmed provider collection. Existing prepaid\nbalance is carried forward as account credit.",
    )
    "Switch from prepaid to a selected postpaid tier.\nA verified email is always required. Free needs no billing profile or payment\nprovider; paid tiers require confirmed provider collection. Existing prepaid\nbalance is carried forward as account credit."


class PromoteToPaidPromoteToPaidPromoteToPaidPayload(PromoteToPaidPayloadDefault):
    typename__: Literal["PromoteToPaidPayload"] = Field(alias="__typename")


class PromoteToPaidPromoteToPaidValidationError(ValidationErrorDefault):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class PromoteToPaidPromoteToPaidAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


PromoteToPaid.model_rebuild()
