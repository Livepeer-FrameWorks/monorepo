# @livepeer-frameworks/api

## 0.3.0

### Minor Changes

- Start the 0.3 SDK line, generated from the whole public GraphQL schema. Every public root field that is not deprecated, and every argument-taking field reached from `Query` through single objects, has a generated operation (`<Op>Document` in TypeScript, a function in Go, a method in Python) with a default selection; the 44 hand-written operations stay as overrides. All 13 public subscriptions are generated in every SDK. A new `@livepeer-frameworks/api/select` entry builds typed custom selections over the schema and sends them through the client's transport, auth, retries, and typed errors. Breaking: `ListPushTargets` no longer selects `stream.id`; its result carries `stream.pushTargets` only. The schema compatibility gate covers the whole public schema from FrameWorks v0.3.11 on.

## 0.2.0

### Minor Changes

- Start the breaking 0.2 SDK line. The Python client now defaults to the hosted Bridge endpoint and exposes stable domain models such as `Stream` instead of generated operation-path class names. GraphQL schema descriptions are preserved as TypeScript JSDoc, Go documentation, and Python docstrings/Pydantic metadata so IDE completion and hover help explain operations, models, and fields in every SDK.
