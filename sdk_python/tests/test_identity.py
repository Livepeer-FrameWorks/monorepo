"""Document identity is shared with the generator and does not trust operation names."""

from typing import Any

import pytest

from conftest import Scripted, load_fixture
from livepeer_frameworks import (
    AsyncFrameWorksClient,
    FrameWorksClient,
    UnsupportedOperationError,
)
from livepeer_frameworks._identity import document_identity


@pytest.mark.parametrize(
    "op", load_fixture("operations.json")["operations"], ids=lambda op: op["name"]
)
def test_catalog_identity(op: dict[str, Any]) -> None:
    assert document_identity(op["document"], op["name"]) == op["identity"]


def responses() -> dict[str, list[dict[str, Any]]]:
    return {
        "ServerInfo": [
            {"body": {"data": {"serverInfo": {"version": "v0.3.11", "features": []}}}}
        ]
        * 2,
        "GetBillingLifecycle": [{"body": {"data": {"tenant": {"id": "tenant"}}}}] * 2,
    }


def test_same_name_custom_document_sync() -> None:
    scripted = Scripted(responses())
    with FrameWorksClient(
        "https://identity-sync.test/graphql", http_client=scripted.sync_client()
    ) as client:
        client.execute("query GetBillingLifecycle { tenant { id } }")
        client.execute("query GetBillingLifecycle { tenant { ...Missing } }")
        with pytest.raises(UnsupportedOperationError):
            client.get_billing_lifecycle()
    assert scripted.sent("GetBillingLifecycle") == 2


@pytest.mark.asyncio
async def test_same_name_custom_document_async() -> None:
    scripted = Scripted(responses())
    async with AsyncFrameWorksClient(
        "https://identity-async.test/graphql", http_client=scripted.async_client()
    ) as client:
        await client.execute("query GetBillingLifecycle { tenant { id } }")
        with pytest.raises(UnsupportedOperationError):
            await client.get_billing_lifecycle()
    assert scripted.sent("GetBillingLifecycle") == 1


@pytest.mark.parametrize(
    "case",
    load_fixture("document_identity.json")["cases"],
    ids=lambda case: case["name"],
)
def test_shared_identity_fixtures(case: dict[str, Any]) -> None:
    assert (
        document_identity(case["document"], case["operationName"]) == case["identity"]
    )
