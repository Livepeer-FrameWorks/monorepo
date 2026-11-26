from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (  # noqa: F401
    AuthErrorDefault,
    ChangeBillingTierPayloadDefault,
    ChangeBillingTierPayloadDefaultAppliedTier,
    ChangeBillingTierPayloadDefaultPendingTier,
    ValidationErrorDefault,
)


class ConfirmFreeCancellation(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    confirm_free_cancellation: Annotated[
        Union[
            "ConfirmFreeCancellationConfirmFreeCancellationChangeBillingTierPayload",
            "ConfirmFreeCancellationConfirmFreeCancellationValidationError",
            "ConfirmFreeCancellationConfirmFreeCancellationAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(alias="confirmFreeCancellation")


class ConfirmFreeCancellationConfirmFreeCancellationChangeBillingTierPayload(
    ChangeBillingTierPayloadDefault
):
    typename__: Literal["ChangeBillingTierPayload"] = Field(alias="__typename")


class ConfirmFreeCancellationConfirmFreeCancellationValidationError(
    ValidationErrorDefault
):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class ConfirmFreeCancellationConfirmFreeCancellationAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


ConfirmFreeCancellation.model_rebuild()
