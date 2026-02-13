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

  it("accepts UUID route params for backwards compatibility", () => {
    expect(
      resolveOperationalStreamId({
        routeParamId: "123e4567-e89b-12d3-a456-426614174000",
      })
    ).toBe("123e4567-e89b-12d3-a456-426614174000");
  });

  it("does not fail-open on non-UUID route params", () => {
    expect(resolveOperationalStreamId({ routeParamId: "U3RyZWFtOjEyMw==" })).toBe("");
  });

  it("validates UUID shape", () => {
    expect(isUuid("123e4567-e89b-12d3-a456-426614174000")).toBe(true);
    expect(isUuid("not-a-uuid")).toBe(false);
  });
});
