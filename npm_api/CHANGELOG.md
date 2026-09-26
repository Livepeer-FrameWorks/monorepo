# @livepeer-frameworks/api

## 0.4.0

### Minor Changes

- f22d2b9: Start the 0.4 SDK line. Stream operations need only the `streams:*` scopes: `StreamFields` (used by `ListStreams`, `GetStream`, `CreateStream`, `UpdateStream`, and `RefreshStreamKey`) no longer selects `metrics`, which resolves from analytics and needs `analytics:read` on an API token. Live state moves to two new operations, `GetStreamMetrics` and `ListStreamMetrics`, which select `StreamMetricsFields`. Breaking: code that read `metrics` from a stream result reads it from `GetStreamMetrics` or `ListStreamMetrics` instead.

  Stream keys are publishing credentials, so on an API token they need `streams:write`: without it `Stream.streamKey` is null with a `FORBIDDEN` error at its path, and `streamKeysConnection` is refused. Stream reads need `streams:read` or `streams:write`. `StreamFields` no longer selects `streamKey`, so `ListStreams`, `GetStream`, and `UpdateStream` return no key; `CreateStream` and `RefreshStreamKey` return it as a `StreamWithKey` (the `StreamWithKeyFields` fragment), and the new `GetStreamKey` operation reads the key of one stream. Breaking: code that read `streamKey` from `ListStreams`, `GetStream`, or `UpdateStream` calls `GetStreamKey` with a `streams:write` token instead.

  A response that carries data plus errors confined to fields below a returned root field (a nullable field the server set to null, such as `metrics` for a token without `analytics:read`) now succeeds with its data in all three SDKs, and the errors go to a partial-errors handler: `onPartialErrors` on the client options or per request in TypeScript, `ClientOptions.OnPartialErrors` or `WithPartialErrors(ctx, ...)` in Go, and `on_partial_errors=` on the client or per call in Python. Errors without a path, errors that null a root field, and `UNAUTHORIZED`, `RATE_LIMITED`, and document errors still fail the call. Before, a mutation that committed could raise a `GraphQLError` because such a field failed.

## 0.3.0

### Minor Changes

- Start the 0.3 SDK line, generated from the whole public GraphQL schema. Every public root field that is not deprecated, and every argument-taking field reached from `Query` through single objects, has a generated operation (`<Op>Document` in TypeScript, a function in Go, a method in Python) with a default selection; the 44 hand-written operations stay as overrides. All 13 public subscriptions are generated in every SDK. A new `@livepeer-frameworks/api/select` entry builds typed custom selections over the schema and sends them through the client's transport, auth, retries, and typed errors. Breaking: `ListPushTargets` no longer selects `stream.id`; its result carries `stream.pushTargets` only. The schema compatibility gate covers the whole public schema from FrameWorks v0.3.11 on.

## 0.2.0

### Minor Changes

- Start the breaking 0.2 SDK line. The Python client now defaults to the hosted Bridge endpoint and exposes stable domain models such as `Stream` instead of generated operation-path class names. GraphQL schema descriptions are preserved as TypeScript JSDoc, Go documentation, and Python docstrings/Pydantic metadata so IDE completion and hover help explain operations, models, and fields in every SDK.
