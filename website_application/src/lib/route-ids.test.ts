import { describe, expect, it } from "vitest";
import { isUuid, resolveOperationalStreamId } from "./route-ids";

describe("route id helpers", () => {
  it("prefers stream UUID from GraphQL stream payload", () => {
    expect(
      resolveOperationalStreamId({
        routeParamId: "U3RyZWFtOmFiYw==",
        streamUuid: "123e4567-e89b-12d3-a456-426614174000",
      })
    ).toBe("123e4567-e89b-12d3-a456-426614174000");
  });

  it("resolves stream Relay IDs to their raw UUID", () => {
    expect(
      resolveOperationalStreamId({
        routeParamId: btoa("Stream:5eedfeed-11fe-ca57-feed-11feca570001"),
      })
    ).toBe("5eedfeed-11fe-ca57-feed-11feca570001");
  });

  it("ignores non-UUID stream payload ids and falls back to route param UUID", () => {
    expect(
      resolveOperationalStreamId({
        routeParamId: "123e4567-e89b-12d3-a456-426614174000",
        streamUuid: "live",
      })
    ).toBe("123e4567-e89b-12d3-a456-426614174000");
  });

  it("accepts raw UUID route params", () => {
    expect(
      resolveOperationalStreamId({
        routeParamId: "123e4567-e89b-12d3-a456-426614174000",
      })
    ).toBe("123e4567-e89b-12d3-a456-426614174000");
  });

  it("does not fail-open on non-UUID route params", () => {
    expect(resolveOperationalStreamId({ routeParamId: "U3RyZWFtOjEyMw==" })).toBe("");
  });

  it("does not resolve Relay IDs for other types", () => {
    expect(
      resolveOperationalStreamId({
        routeParamId: btoa("Clip:5eedfeed-11fe-ca57-feed-11feca570001"),
      })
    ).toBe("");
  });

  it("validates UUID shape", () => {
    expect(isUuid("123e4567-e89b-12d3-a456-426614174000")).toBe(true);
    expect(isUuid("5eedfeed-11fe-ca57-feed-11feca570001")).toBe(true);
    expect(isUuid("not-a-uuid")).toBe(false);
  });
});
