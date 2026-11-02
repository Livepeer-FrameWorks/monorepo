# @livepeer-frameworks/player-core

## 0.7.0

### Minor Changes

- 08f04dd: `DEFAULT_GATEWAY_URL` is exported, so a page can tell whether its `gatewayUrl` is the official FrameWorks Gateway.

### Patch Changes

- f5c8759: With pre-resolved `endpoints`, the player rules out Mist only when the edge answers `json_<name>.js` with HTTP 404 or a reply that is not Mist JSON. A network error or another HTTP error repeats the lookup with backoff; once Mist answers, the player takes that edge's sources and tracks and starts stream-state polling. A Mist `Could not retrieve stream` reply, which Mist sends for a name it does not know, plays the URL without opening stream-state or metadata sockets for that name.
- Updated dependencies [30d2f3e]
  - @livepeer-frameworks/api@0.5.0

## 0.6.4

### Patch Changes

- 4721b31: Pre-resolved endpoints on a host without MistServer no longer open Mist stream-state or metadata sockets. Set `baseUrl` to keep both on a known Mist edge.
- f8d56f2: A pre-resolved endpoint on a Mist edge keeps polling stream state when Mist answers with an error such as "Stream is offline", so the player picks the stream up when it comes online.

## 0.6.3

### Patch Changes

- Updated dependencies [f22d2b9]
  - @livepeer-frameworks/api@0.4.0

## 0.6.2

### Patch Changes

- Updated dependencies
  - @livepeer-frameworks/api@0.3.0

## 0.6.1

### Patch Changes

- Updated dependencies
  - @livepeer-frameworks/api@0.2.0

## 0.6.0

### Minor Changes

- Viewer and ingest resolution send the typed `ResolveViewerEndpoint` and `ResolveIngestEndpoint` documents from
  `@livepeer-frameworks/api`, now a dependency of the core packages. Each gateway's `serverInfo` is probed once per
  page: a gateway older than FrameWorks v0.3.11, or one without `serverInfo`, is refused with `ServerTooOldError`
  (`GatewayClient.resolve()` and `IngestClient.resolve()` reject with it, and the status event carries its message).
  Development builds and release candidates are not checked, and a probe that cannot reach the gateway does not block
  resolution. The `GatewayClient` and `IngestClient` APIs are unchanged.
