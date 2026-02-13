import { describe, expect, it } from "vitest";
import { getBreadcrumbs, getRouteInfo } from "./navigation";

describe("navigation route resolution", () => {
  it("resolves known static routes", () => {
    expect(getRouteInfo("/streams")?.name).toBe("Streams");
  });

  it("resolves dynamic routes with route params", () => {
    const route = getRouteInfo("/streams/U3RyZWFtOjEyMw==");
    expect(route?.name).toBe("Stream Details");
    expect(route?.parent).toBe("Content");
  });

  it("ignores query params and trailing slash in matching", () => {
    const route = getRouteInfo("/streams/123e4567-e89b-12d3-a456-426614174000/analytics/?tab=x");
    expect(route?.name).toBe("Stream Analytics");
  });

  it("builds breadcrumbs for dynamic stream health routes", () => {
    const breadcrumbs = getBreadcrumbs("/streams/abc/health");
    expect(breadcrumbs.map((b) => b.name)).toEqual([
      "Dashboard",
      "Content",
      "Streams",
      "Stream Health",
    ]);
  });
});
