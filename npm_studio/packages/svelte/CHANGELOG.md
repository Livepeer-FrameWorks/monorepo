# @livepeer-frameworks/streamcrafter-svelte

## 0.4.2

### Patch Changes

- @livepeer-frameworks/streamcrafter-core@0.4.2

## 0.4.1

### Patch Changes

- @livepeer-frameworks/streamcrafter-core@0.4.1

## 0.4.0

### Minor Changes

- Viewer and ingest resolution send the typed `ResolveViewerEndpoint` and `ResolveIngestEndpoint` documents from
  `@livepeer-frameworks/api`, now a dependency of the core packages. Each gateway's `serverInfo` is probed once per
  page: a gateway older than FrameWorks v0.3.11, or one without `serverInfo`, is refused with `ServerTooOldError`
  (`GatewayClient.resolve()` and `IngestClient.resolve()` reject with it, and the status event carries its message).
  Development builds and release candidates are not checked, and a probe that cannot reach the gateway does not block
  resolution. The `GatewayClient` and `IngestClient` APIs are unchanged.

### Patch Changes

- Updated dependencies
  - @livepeer-frameworks/streamcrafter-core@0.4.0
