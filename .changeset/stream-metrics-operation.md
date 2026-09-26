---
"@livepeer-frameworks/api": minor
---

Start the 0.4 SDK line. Stream operations need only the `streams:*` scopes: `StreamFields` (used by `ListStreams`, `GetStream`, `CreateStream`, `UpdateStream`, and `RefreshStreamKey`) no longer selects `metrics`, which resolves from analytics and needs `analytics:read` on an API token. Live state moves to two new operations, `GetStreamMetrics` and `ListStreamMetrics`, which select `StreamMetricsFields`. Breaking: code that read `metrics` from a stream result reads it from `GetStreamMetrics` or `ListStreamMetrics` instead.

A response that carries data plus errors confined to fields below a returned root field (a nullable field the server set to null, such as `metrics` for a token without `analytics:read`) now succeeds with its data in all three SDKs, and the errors go to a partial-errors handler: `onPartialErrors` on the client options or per request in TypeScript, `ClientOptions.OnPartialErrors` or `WithPartialErrors(ctx, ...)` in Go, and `on_partial_errors=` on the client or per call in Python. Errors without a path, errors that null a root field, and `UNAUTHORIZED`, `RATE_LIMITED`, and document errors still fail the call. Before, a mutation that committed could raise a `GraphQLError` because such a field failed.
