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


class ChangeBillingTier(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    change_billing_tier: Annotated[
        Union[
            "ChangeBillingTierChangeBillingTierChangeBillingTierPayload",
            "ChangeBillingTierChangeBillingTierValidationError",
            "ChangeBillingTierChangeBillingTierAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="changeBillingTier",
        description="Change the postpaid billing tier. Upgrades start a fresh month and credit\nthe unused paid fee; provider or metering reconciliation can leave them pending.\nEnterprise and upgrades to a lower base price require an operator arrangement.\nDowngrades take effect at the current billing period end.",
    )
    "Change the postpaid billing tier. Upgrades start a fresh month and credit\nthe unused paid fee; provider or metering reconciliation can leave them pending.\nEnterprise and upgrades to a lower base price require an operator arrangement.\nDowngrades take effect at the current billing period end."


class ChangeBillingTierChangeBillingTierChangeBillingTierPayload(
    ChangeBillingTierPayloadDefault
):
    typename__: Literal["ChangeBillingTierPayload"] = Field(alias="__typename")


class ChangeBillingTierChangeBillingTierValidationError(ValidationErrorDefault):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class ChangeBillingTierChangeBillingTierAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


ChangeBillingTier.model_rebuild()
