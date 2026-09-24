"""The hand-written Python surface hides generator naming and has useful defaults."""

from __future__ import annotations

import inspect
import typing

import httpx

import livepeer_frameworks.graphql as graphql
from livepeer_frameworks import AsyncFrameWorksClient, DEFAULT_GRAPHQL_URL, FrameWorksClient, expect_result
from livepeer_frameworks._generated.graphql import enums, fragments, input_types
from livepeer_frameworks._generated.graphql.async_client import AsyncGraphQLClient
from livepeer_frameworks._generated.graphql.client import GraphQLClient
from livepeer_frameworks._generated.graphql.create_stream import CreateStreamCreateStreamStream
from livepeer_frameworks._generated.graphql.tenant_events import TenantEventsTenantEventsDataStreamUpdated
from livepeer_frameworks.graphql import CreateStreamInput, Stream


def test_client_defaults_to_hosted_bridge() -> None:
    with FrameWorksClient(http_client=httpx.Client(), check_server=False) as client:
        assert client.url == DEFAULT_GRAPHQL_URL
        assert client.url == "https://bridge.frameworks.network/graphql"


async def test_async_client_defaults_to_hosted_bridge_and_websocket() -> None:
    async with AsyncFrameWorksClient(http_client=httpx.AsyncClient(), check_server=False) as client:
        assert client.url == DEFAULT_GRAPHQL_URL
        assert client.ws_url == "wss://bridge.frameworks.network/graphql/ws"


def test_public_stream_model_hides_generated_operation_path() -> None:
    value = CreateStreamCreateStreamStream.model_construct(typename__="Stream")

    assert Stream.__name__ == "Stream"
    assert expect_result(value, Stream) is value
    assert "CreateStreamCreateStreamStream" not in graphql.__all__
    assert not hasattr(graphql, "CreateStreamCreateStreamStream")


def test_fragment_name_cleanup_does_not_change_wire_aliases() -> None:
    event = TenantEventsTenantEventsDataStreamUpdated.model_validate(
        {"__typename": "StreamUpdated", "streamId": "stream-1", "changedFields": ["name"]}
    )

    assert event.changed_fields == ["name"]
    assert event.model_dump(by_alias=True)["changedFields"] == ["name"]


def test_schema_descriptions_are_available_to_python_tools() -> None:
    assert CreateStreamInput.model_fields["name"].description == "Human-readable name for the stream."
    assert Stream.model_fields["playback_id"].description == "Public identifier for playback URLs."
    assert Stream.__doc__ and "core entity for broadcasting" in Stream.__doc__
    assert inspect.getdoc(FrameWorksClient.create_stream) == "Create a new stream for live broadcasting."


# The hand-maintained export list before it was generated; each name stays public.
_PREVIOUS_PUBLIC_NAMES = frozenset(
    """
    Clip ClipCreationMode ClipEffectiveRetention ClipPlaybackPolicy ClipThumbnailAssets
    CompleteVodUploadInput ConnectionInput CreateClipInput CreateDeveloperTokenInput
    CreatePushTargetInput CreateSigningKeyInput CreateStreamInput CreateStreamKeyInput
    CreateVodUploadInput DVRChapterMode DVRChapterRef DVRChapterState DVRRequest DeleteSuccess
    DeveloperToken EffectiveRetention EventArtifact EventArtifactKind EventCustomDomainFailureReason
    EventIngestProtocol EventMediaFailureReason EventMoney EventMultistreamStatus
    EventPaymentFailureReason EventSuspensionReason GraphQLClient IngestEndpoint IngestEndpointKind
    IngestMode MediaIngestProtocol MediaViewerProtocol MonitoringToggle PageInfo
    PlaybackJwtClaimRequirementInput PlaybackJwtPolicyInput PlaybackPolicy PlaybackPolicyInput
    PlaybackPolicyJwt PlaybackPolicyJwtRequiredClaimsJson PlaybackPolicyType PlaybackPolicyWebhook
    PlaybackWebhookPolicyInput PullSourceInput PushTarget RetentionSource SetPlaybackPolicyInput
    SigningKey SigningKeyAlgorithm SigningKeyStatus SortDirection SourceLocationMode
    SourceLocationClusterInput SourceLocationInput StorageArtifact StorageArtifactKind
    StorageArtifactSortField StorageArtifactThumbnailAssets StorageArtifactsInput Stream StreamKey
    StreamMetrics StreamPlaybackPolicy StreamPullSource StreamStatus TestPlaybackAccessInput
    ThumbnailAssets TimeRangeInput UpdatePushTargetInput UpdateStreamInput ViewerEndpoint VodAsset
    VodAssetEffectiveRetention VodAssetPlaybackPolicy VodAssetStatus VodAssetThumbnailAssets
    VodUploadCompletedPart
    """.split()
)


def _signature_types(annotation: object, found: set[type]) -> None:
    """Collects the enums and input types an annotation names, and those the
    fields of each input type name."""
    if isinstance(annotation, type) and annotation.__module__ in (input_types.__name__, enums.__name__):
        if annotation in found:
            return
        found.add(annotation)
        if annotation.__module__ == input_types.__name__:
            for hint in typing.get_type_hints(annotation).values():
                _signature_types(hint, found)
        return
    for arg in typing.get_args(annotation):
        _signature_types(arg, found)


def _client_signature_types() -> set[type]:
    found: set[type] = set()
    for client in (GraphQLClient, AsyncGraphQLClient):
        for name, method in inspect.getmembers(client, inspect.isfunction):
            if name.startswith("_"):
                continue
            for hint in typing.get_type_hints(method).values():
                _signature_types(hint, found)
    return found


def test_every_type_a_client_method_takes_is_public() -> None:
    required = _client_signature_types()

    assert any(t.__name__ == "BootstrapEdgeInput" for t in required)
    for type_ in required:
        assert getattr(graphql, type_.__name__, None) is type_, type_.__name__
        assert type_.__name__ in graphql.__all__


def test_every_generated_enum_and_input_type_is_public() -> None:
    for module in (enums, input_types):
        for name, value in inspect.getmembers(module, inspect.isclass):
            if value.__module__ == module.__name__:
                assert getattr(graphql, name, None) is value, name


def test_previous_public_names_stay_importable() -> None:
    missing = sorted(name for name in _PREVIOUS_PUBLIC_NAMES if not hasattr(graphql, name))
    assert missing == []
    assert _PREVIOUS_PUBLIC_NAMES <= set(graphql.__all__)
    assert graphql.WebhookDeliveryStatus.__module__ == enums.__name__
    assert graphql.IncidentFilterInput.__module__ == input_types.__name__
    assert graphql.MediaPlacementOptionsFilter.__module__ == input_types.__name__


def test_response_containers_and_default_fragments_are_not_public() -> None:
    allowed = {enums.__name__, input_types.__name__, fragments.__name__, GraphQLClient.__module__}
    for name in graphql.__all__:
        assert getattr(graphql, name).__module__ in allowed, name
    assert not hasattr(graphql, "CreateStream")
    assert not hasattr(graphql, "BootstrapEdgeBootstrapEdgeBootstrapEdgeResponse")
    assert not hasattr(graphql, "BootstrapEdgeResponseDefault")
    # Error members are raised by expect_result, not returned.
    assert not hasattr(graphql, "ValidationError")
    assert not hasattr(graphql, "RateLimitError")
    assert list(graphql.__all__) == sorted(graphql.__all__)
