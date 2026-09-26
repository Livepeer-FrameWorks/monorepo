# FrameWorks Go SDK

Typed Go client for the FrameWorks GraphQL API: a generated function for
every public root field and argument-taking field, plus retries, typed errors, a server version check,
pagination, subscriptions, VOD uploads, playback token signing, and webhook
verification.

```sh
go get github.com/Livepeer-FrameWorks/sdk-go
```

```go
client, err := frameworks.NewClient(frameworks.ClientOptions{
	URL:   "https://bridge.example.com/graphql",
	Token: apiToken,
})
if err != nil {
	log.Fatal(err)
}
resp, err := frameworks.CreateStream(ctx, client, frameworks.CreateStreamInput{Name: "launch-event"})
if err != nil {
	log.Fatal(err)
}
stream, err := frameworks.ExpectResult[*frameworks.CreateStreamCreateStream](resp.CreateStream)
```

More examples are in `examples/`; the guide is at
https://logbook.frameworks.network/builders/sdks.

## Custom documents

Every public root field and argument-taking field has a generated function
with a default selection. For other selections, write your own operation
documents against the public schema,
[`pkg/graphql/public/schema.public.graphql`](https://github.com/Livepeer-FrameWorks/monorepo/blob/master/pkg/graphql/public/schema.public.graphql),
and generate typed functions with [genqlient](https://github.com/Khan/genqlient)
in your own module, binding the custom scalars as the SDK does:

```yaml
# genqlient.yaml
schema: schema.public.graphql
operations: [operations/*.graphql]
generated: frameworksops/generated.go
package: frameworksops
bindings:
  Time: { type: time.Time }
  JSON: { type: encoding/json.RawMessage }
  Currency: { type: string }
  Money: { type: string }
```

`*frameworks.Client` implements genqlient's `graphql.Client`, so the generated
functions take it directly and run through `MakeRequest`: the same
authentication, retries, typed errors, and server version check as the SDK's
own functions. A document can also be sent without code generation:

```go
var data struct {
	Stream *struct {
		Name string `json:"name"`
	} `json:"stream"`
}
err := client.MakeRequest(ctx, &graphql.Request{
	Query:     `query StreamName($id: ID!) { stream(id: $id) { name } }`,
	OpName:    "StreamName",
	Variables: map[string]any{"id": streamID},
}, &graphql.Response{Data: &data})
```

`MakeRequest` runs queries and mutations; subscriptions use a
`SubscriptionClient`. Give custom operations names of your own: the server
version check looks up an SDK operation's release by its name.

## Scopes and partial errors

An API token carries scopes. The stream functions (`ListStreams`, `GetStream`,
`CreateStream`, `UpdateStream`, `RefreshStreamKey`) select only stream fields,
so `streams:read` and `streams:write` cover them. Live state
(`Stream.metrics`) comes from analytics and needs `analytics:read`: read it
with `GetStreamMetrics` or `ListStreamMetrics`. A stream key lets its holder
publish, so it needs `streams:write`: `CreateStream` and `RefreshStreamKey`
return it, `GetStreamKey` reads it, and `ListStreams`, `GetStream`, and
`UpdateStream` do not select it.

When a field below a returned root field fails (for example `metrics`
selected with a token that lacks `analytics:read`), the server sets it to
null and reports an error at its path. The call returns the data and a nil
error, and hands those errors to `ClientOptions.OnPartialErrors` or to a
handler set for one call:

```go
ctx = frameworks.WithPartialErrors(ctx, func(p frameworks.PartialErrors) {
	log.Printf("%s: %d field errors", p.Operation, len(p.Errors))
})
```

Errors without a path, errors that null a root field, and `UNAUTHORIZED`,
`RATE_LIMITED`, and document errors return a typed error.

## Versions

The TypeScript, Go, and Python SDKs share one version. Before 1.0 each minor
version is its own compatibility line, and `MinServerVersion` is the oldest
FrameWorks release the line supports; against an older server every call
returns `*ServerTooOldError`.

This repository is a mirror of `sdk_go/` in
[Livepeer-FrameWorks/monorepo](https://github.com/Livepeer-FrameWorks/monorepo);
open issues and pull requests there.
