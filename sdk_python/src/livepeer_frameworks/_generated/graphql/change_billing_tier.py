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
        description="Change the postpaid billing tier. Upgrades start a fresh month and offset the\nreplacement fee with the unused retained paid old fee, capped at the replacement\nprice, without cash refund or spendable balance. Provider or metering\nreconciliation can leave them pending.\nEnterprise purchases and moves to a higher tier with a lower base price require an operator arrangement.\nDowngrades take effect at the current billing period end.\nA refusal returns ValidationError whose code is one of PENDING_CHECKOUT,\nPENDING_FREE_FALLBACK, PENDING_UPGRADE, OUTSTANDING_GRANT, ACCOUNT_CLOSED,\nDOWNGRADE_HELD, PAID_TERM_ENDING, PROVIDER_SUBSCRIPTION_LIVE,\nTIER_NOT_SELF_SERVICE, INVOICES_OUTSTANDING, COLLECTION_SETUP_REQUIRED or\nBILLING_PROFILE_REQUIRED, with a message to show the customer.",
    )
    "Change the postpaid billing tier. Upgrades start a fresh month and offset the\nreplacement fee with the unused retained paid old fee, capped at the replacement\nprice, without cash refund or spendable balance. Provider or metering\nreconciliation can leave them pending.\nEnterprise purchases and moves to a higher tier with a lower base price require an operator arrangement.\nDowngrades take effect at the current billing period end.\nA refusal returns ValidationError whose code is one of PENDING_CHECKOUT,\nPENDING_FREE_FALLBACK, PENDING_UPGRADE, OUTSTANDING_GRANT, ACCOUNT_CLOSED,\nDOWNGRADE_HELD, PAID_TERM_ENDING, PROVIDER_SUBSCRIPTION_LIVE,\nTIER_NOT_SELF_SERVICE, INVOICES_OUTSTANDING, COLLECTION_SETUP_REQUIRED or\nBILLING_PROFILE_REQUIRED, with a message to show the customer."


class ChangeBillingTierChangeBillingTierChangeBillingTierPayload(
    ChangeBillingTierPayloadDefault
):
    typename__: Literal["ChangeBillingTierPayload"] = Field(alias="__typename")


class ChangeBillingTierChangeBillingTierValidationError(ValidationErrorDefault):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class ChangeBillingTierChangeBillingTierAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


ChangeBillingTier.model_rebuild()
