# @livepeer-frameworks/player-react

## 0.7.1

### Patch Changes

- @livepeer-frameworks/player-core@0.7.1

## 0.7.0

### Minor Changes

- 89afac9: `<Player>` forwards a `ref` to its `PlayerController`. The ref receives the controller once it exists and `null` after it is destroyed, so a page can read stats, tracks and events from the same controller the player uses, as `controller` already allows on `<fw-player>`.

### Patch Changes

- Updated dependencies [08f04dd]
- Updated dependencies [f5c8759]
  - @livepeer-frameworks/player-core@0.7.0

## 0.6.4

### Patch Changes

- Updated dependencies [4721b31]
- Updated dependencies [f8d56f2]
  - @livepeer-frameworks/player-core@0.6.4

## 0.6.3

### Patch Changes

- @livepeer-frameworks/player-core@0.6.3

## 0.6.2

### Patch Changes

- @livepeer-frameworks/player-core@0.6.2

## 0.6.1

### Patch Changes

- @livepeer-frameworks/player-core@0.6.1

## 0.6.0

### Minor Changes

- Viewer and ingest resolution send the typed `ResolveViewerEndpoint` and `ResolveIngestEndpoint` documents from
  `@livepeer-frameworks/api`, now a dependency of the core packages. Each gateway's `serverInfo` is probed once per
  page: a gateway older than FrameWorks v0.3.11, or one without `serverInfo`, is refused with `ServerTooOldError`
  (`GatewayClient.resolve()` and `IngestClient.resolve()` reject with it, and the status event carries its message).
  Development builds and release candidates are not checked, and a probe that cannot reach the gateway does not block
  resolution. The `GatewayClient` and `IngestClient` APIs are unchanged.

### Patch Changes

- Updated dependencies
  - @livepeer-frameworks/player-core@0.6.0
