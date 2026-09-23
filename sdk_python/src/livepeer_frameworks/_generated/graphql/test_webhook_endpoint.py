from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (  # noqa: F401
    AuthErrorDefault,
    NotFoundErrorDefault,
    RateLimitErrorDefault,
    WebhookTestResultDefault,
    WebhookTestResultDefaultAttempt,
    WebhookTestResultDefaultDelivery,
)


class TestWebhookEndpoint(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    test_webhook_endpoint: Annotated[
        Union[
            "TestWebhookEndpointTestWebhookEndpointWebhookTestResult",
            "TestWebhookEndpointTestWebhookEndpointNotFoundError",
            "TestWebhookEndpointTestWebhookEndpointRateLimitError",
            "TestWebhookEndpointTestWebhookEndpointAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="testWebhookEndpoint",
        description="Send a signed webhook.test event to the endpoint and wait for the result.\nAt most one test per endpoint every 10 seconds.",
    )
    "Send a signed webhook.test event to the endpoint and wait for the result.\nAt most one test per endpoint every 10 seconds."


class TestWebhookEndpointTestWebhookEndpointWebhookTestResult(WebhookTestResultDefault):
    typename__: Literal["WebhookTestResult"] = Field(alias="__typename")


class TestWebhookEndpointTestWebhookEndpointNotFoundError(NotFoundErrorDefault):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class TestWebhookEndpointTestWebhookEndpointRateLimitError(RateLimitErrorDefault):
    typename__: Literal["RateLimitError"] = Field(alias="__typename")


class TestWebhookEndpointTestWebhookEndpointAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


TestWebhookEndpoint.model_rebuild()
