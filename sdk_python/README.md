# livepeer-frameworks

Typed Python client for the [FrameWorks](https://frameworks.network) GraphQL API. It ships a sync
and an async client generated from FrameWorks' curated public operations, with pydantic models,
retries, pagination, typed errors, a VOD upload helper, playback token signing, and webhook
verification.

```sh
pip install livepeer-frameworks
```

```python
from livepeer_frameworks import FrameWorksClient, expect_result, paginate_relay
from livepeer_frameworks.graphql import ConnectionInput, CreateStreamCreateStreamStream, CreateStreamInput

with FrameWorksClient("https://bridge.example.com/graphql", token="YOUR_API_TOKEN") as fw:
    created = fw.create_stream(input=CreateStreamInput(name="Studio A"))
    # Returns the Stream member or raises ResultError for ValidationError/AuthError.
    stream = expect_result(created.create_stream, CreateStreamCreateStreamStream)
    print(stream.stream_key, stream.playback_id)

    for s in paginate_relay(
        lambda page: fw.list_streams(page=ConnectionInput.model_validate(page)).streams_connection
    ):
        print(s.name)
```

The client reads no environment variables; pass the URL and token explicitly. Per-call options are
keyword arguments of the generated methods: `idempotency_key=` (sent as `Idempotency-Key`; paid
mutations settled with x402 need one) and `playback_token=` (sent as `X-Frameworks-Playback-JWT`).

Queries are retried on network errors and HTTP 408, 429, and 5xx. A mutation is retried only when
the request provably never reached the server: the connection could not be established, or the
gateway answered 429. It is never sent again after a timeout, a dropped connection, or a 5xx,
because the gateway does not deduplicate replayed mutations.

## Versions

The TypeScript, Go, and Python SDKs share one version. Until FrameWorks 1.0 the SDKs stay at 0.x,
and each minor line supports the FrameWorks releases listed for it; this line needs FrameWorks
v0.3.11 or later. Against an older stable release the client raises `ServerTooOldError` before
sending a call.

Documentation: <https://logbook.frameworks.network/builders/sdks>
