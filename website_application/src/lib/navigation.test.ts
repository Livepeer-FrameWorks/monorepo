import { readdirSync } from "node:fs";
import { join, relative } from "node:path";

import { describe, expect, it } from "vitest";

import { getAllRoutes, getBreadcrumbs, getRouteInfo, navigationConfig } from "./navigation";

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

function collectPageRoutes(root: string): Set<string> {
  const discovered = new Set<string>();

  function walk(currentDir: string) {
    for (const entry of readdirSync(currentDir, { withFileTypes: true })) {
      const fullPath = join(currentDir, entry.name);
      if (entry.isDirectory()) {
        walk(fullPath);
        continue;
      }
      if (entry.isFile() && entry.name === "+page.svelte") {
        const routeDir = relative(root, currentDir);
        const routePath = routeDir ? `/${routeDir.replaceAll("\\", "/")}` : "/";
        discovered.add(routePath);
      }
    }
  }

  walk(root);
  return discovered;
}

describe("navigation route hygiene", () => {
  it("maps nested and dynamic routes for deep links", () => {
    expect(getRouteInfo("/infrastructure/cluster-a")?.name).toBe("Cluster Details");
    expect(getRouteInfo("/streams/stream-1/analytics")?.name).toBe("Stream Analytics");
    expect(getRouteInfo("/messages/thread-1")?.name).toBe("Conversation");
    expect(getRouteInfo("/analytics/audience/?tab=geo")?.name).toBe("Audience");
  });

  it("builds breadcrumbs for dynamic infrastructure routes", () => {
    expect(getBreadcrumbs("/infrastructure/cluster-a")).toEqual([
      { name: "Dashboard", href: "/" },
      { name: "Infrastructure" },
      { name: "Overview", href: "/infrastructure" },
      { name: "Cluster Details" },
    ]);
  });

  it("keeps active sidebar routes aligned with real Svelte routes", () => {
    const routeRoot = join(process.cwd(), "src", "routes");
    const pageRoutes = collectPageRoutes(routeRoot);

    const activeRoutes = Object.values(navigationConfig)
      .flatMap((section) => Object.values(section.children ?? {}))
      .filter((item) => item.active === true && item.href)
      .map((item) => item.href as string);

    for (const route of activeRoutes) {
      expect(pageRoutes.has(route), `Missing route for active nav href: ${route}`).toBe(true);
    }
  });

  it("includes hidden infrastructure routes in route metadata", () => {
    const routePaths = getAllRoutes().map((route) => route.path);
    expect(routePaths).toContain("/infrastructure/marketplace");
  });
});
