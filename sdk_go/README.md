# FrameWorks Go SDK

Typed Go client for the FrameWorks GraphQL API: one generated function per
public operation, plus retries, typed errors, a server version check,
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

## Versions

The TypeScript, Go, and Python SDKs share one version. Before 1.0 each minor
version is its own compatibility line, and `MinServerVersion` is the oldest
FrameWorks release the line supports; against an older server every call
returns `*ServerTooOldError`.

This repository is a mirror of `sdk_go/` in
[Livepeer-FrameWorks/monorepo](https://github.com/Livepeer-FrameWorks/monorepo);
open issues and pull requests there.
