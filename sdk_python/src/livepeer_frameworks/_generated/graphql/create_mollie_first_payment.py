from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (
    AuthErrorDefault,
    MollieFirstPaymentDefault,
    NotFoundErrorDefault,
    ValidationErrorDefault,
)


class CreateMollieFirstPayment(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    create_mollie_first_payment: Annotated[
        Union[
            "CreateMollieFirstPaymentCreateMollieFirstPaymentMollieFirstPayment",
            "CreateMollieFirstPaymentCreateMollieFirstPaymentValidationError",
            "CreateMollieFirstPaymentCreateMollieFirstPaymentNotFoundError",
            "CreateMollieFirstPaymentCreateMollieFirstPaymentAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="createMollieFirstPayment",
        description="Create a Mollie first payment to establish a mandate.\nFor iDEAL: User pays via bank → SEPA Direct Debit mandate is created.\nFor card: User enters card → card mandate is created.\nAfter successful payment, call createMollieSubscription to start recurring billing.",
    )
    "Create a Mollie first payment to establish a mandate.\nFor iDEAL: User pays via bank → SEPA Direct Debit mandate is created.\nFor card: User enters card → card mandate is created.\nAfter successful payment, call createMollieSubscription to start recurring billing."


class CreateMollieFirstPaymentCreateMollieFirstPaymentMollieFirstPayment(
    MollieFirstPaymentDefault
):
    typename__: Literal["MollieFirstPayment"] = Field(alias="__typename")


class CreateMollieFirstPaymentCreateMollieFirstPaymentValidationError(
    ValidationErrorDefault
):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class CreateMollieFirstPaymentCreateMollieFirstPaymentNotFoundError(
    NotFoundErrorDefault
):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class CreateMollieFirstPaymentCreateMollieFirstPaymentAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


CreateMollieFirstPayment.model_rebuild()
