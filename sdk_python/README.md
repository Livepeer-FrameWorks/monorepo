# livepeer-frameworks

Typed Python client for the [FrameWorks](https://frameworks.network) GraphQL API. It ships a sync
and an async client generated from the public GraphQL schema, with a method for every public root
field and argument-taking field, pydantic models, retries, pagination, typed errors, a VOD upload
helper, playback token signing, and webhook verification.

```sh
pip install livepeer-frameworks
```

```python
from livepeer_frameworks import FrameWorksClient, expect_result, paginate_relay
from livepeer_frameworks.graphql import ConnectionInput, CreateStreamInput, Stream

with FrameWorksClient(token="YOUR_API_TOKEN") as fw:
    created = fw.create_stream(input=CreateStreamInput(name="Studio A"))
    # Returns the Stream member or raises ResultError for ValidationError/AuthError.
    stream = expect_result(created.create_stream, Stream)
    print(stream.stream_key, stream.playback_id)

    for s in paginate_relay(
        lambda page: fw.list_streams(page=ConnectionInput.model_validate(page)).streams_connection
    ):
        print(s.name)
```

The client defaults to `https://bridge.frameworks.network/graphql` and reads no environment
variables. Pass `url=` only for a self-hosted or staging Bridge, and pass the token explicitly.
Per-call options are
keyword arguments of the generated methods: `idempotency_key=` (sent as `Idempotency-Key`; paid
mutations settled with x402 need one) and `playback_token=` (sent as `X-Frameworks-Playback-JWT`).

Queries are retried on network errors and HTTP 408, 429, and 5xx. A mutation is retried only when
the request provably never reached the server: the connection could not be established, or the
gateway answered 429. It is never sent again after a timeout, a dropped connection, or a 5xx,
because the gateway does not deduplicate replayed mutations.

## Subscriptions

Every public subscription is an async iterator method on `AsyncFrameWorksClient` (`tenant_events`,
`live_stream_events`, `skipper_chat`, ...) that yields the operation's pydantic model:

```python
async with AsyncFrameWorksClient(token="YOUR_API_TOKEN") as fw:
    async for event in fw.tenant_events(types=["stream.live", "stream.idle"]):
        print(event.tenant_events.type_, event.tenant_events.subject)
```

The sync `FrameWorksClient` has no subscription methods: ariadne-codegen, which generates the
clients, emits subscriptions only for an async client.

## Custom documents

Every public root field and argument-taking field has a generated method with a default
selection. The Python SDK has no typed builder for other selections: ariadne-codegen 0.19's custom
operation builder generates only queries and mutations (no subscriptions), and returns
`dict[str, Any]` with custom scalars undecoded (`Time` arrives as a `str`, where the generated
methods return `datetime`). For other selections, send your own document through the client's
`execute` (queries and mutations; `get_data` returns the data) or the async client's `execute_ws`
(subscriptions), which use the SDK's authentication, retries, and typed errors. Give it an
operation name of your own: the server version check looks up an SDK operation's release by name.

## Scopes and partial errors

An API token carries scopes. The stream methods (`list_streams`, `get_stream`, `create_stream`,
`update_stream`, `refresh_stream_key`) select only stream fields, so `streams:read` and
`streams:write` cover them. Live state (`Stream.metrics`) comes from analytics and needs
`analytics:read`: read it with `get_stream_metrics` or `list_stream_metrics`.

When a field below a returned root field fails (for example `metrics` selected with a token that
lacks `analytics:read`), the server sets it to null and reports an error at its path. The call
returns the data and hands those errors, as a `PartialErrors`, to `on_partial_errors=`, set on the
client or per call:

```python
warnings: list[PartialErrors] = []
fw.list_streams(on_partial_errors=warnings.append)
```

Errors without a path, errors that null a root field, and `UNAUTHORIZED`, `RATE_LIMITED`, and
document errors raise a typed error.

## Versions

The TypeScript, Go, and Python SDKs share one version. Until FrameWorks 1.0 the SDKs stay at 0.x,
and each minor line supports the FrameWorks releases listed for it; this line needs FrameWorks
v0.3.11 or later. Against an older stable release the client raises `ServerTooOldError` before
sending a call.

Documentation: <https://logbook.frameworks.network/builders/sdks>
