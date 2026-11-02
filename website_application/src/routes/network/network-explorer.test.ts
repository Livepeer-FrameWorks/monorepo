import { describe, expect, it } from "vitest";
import {
  livepeerOrchestratorURL,
  networkAPIURL,
  networkExplorerActions,
  observationsFailure,
} from "../../../../packages/map-core/network-explorer";
import { devProxy } from "../../../vite.config.js";

describe("network explorer API URL", () => {
  it("resolves a relative GraphQL URL against the page origin", () => {
    expect(networkAPIURL("/v1/network/livepeer", "/graphql", "http://localhost:5182")).toBe(
      "http://localhost:5182/v1/network/livepeer"
    );
  });

  it("uses the gateway origin of an absolute GraphQL URL", () => {
    expect(
      networkAPIURL(
        "/v1/network/livepeer/diagnostics",
        "https://bridge.frameworks.network/graphql",
        "https://chartroom.frameworks.network"
      )
    ).toBe("https://bridge.frameworks.network/v1/network/livepeer/diagnostics");
  });

  it("falls back to the page origin when no GraphQL URL is configured", () => {
    expect(networkAPIURL("/v1/network/livepeer", "", "http://localhost:3000")).toBe(
      "http://localhost:3000/v1/network/livepeer"
    );
  });
});

describe("dashboard dev proxy", () => {
  it("forwards Bridge REST routes next to GraphQL", () => {
    const proxy = devProxy("http://localhost:18000");
    expect(proxy["/v1"]).toEqual({ target: "http://localhost:18000", changeOrigin: true });
    expect(proxy["/graphql"].target).toBe("http://localhost:18000");
  });
});

describe("observations failure", () => {
  it("reports a deployment without a network observer as not configured", () => {
    for (const error of [
      "network observations are not configured",
      "network scope is not configured",
    ]) {
      expect(observationsFailure(503, { error })).toEqual({
        message: "Network observations are not set up on this deployment.",
        notConfigured: true,
      });
    }
  });

  it("keeps a temporary outage retryable", () => {
    expect(
      observationsFailure(503, { error: "network inventory temporarily unavailable" })
    ).toEqual({
      message: "Observations unavailable (HTTP 503)",
      notConfigured: false,
    });
    expect(observationsFailure(502, null).notConfigured).toBe(false);
  });
});

describe("explorer links", () => {
  it("links an orchestrator address to its Livepeer Explorer page", () => {
    expect(
      livepeerOrchestratorURL(
        "https://explorer.livepeer.org",
        "0x847791CBF03BE716A7FE9DC8C9AFFE17BD49AE5E"
      )
    ).toBe(
      "https://explorer.livepeer.org/accounts/0x847791cbf03be716a7fe9dc8c9affe17bd49ae5e/orchestrating"
    );
    expect(livepeerOrchestratorURL("https://explorer.livepeer.org", "orchestrator.example")).toBe(
      undefined
    );
  });

  it("offers the same next steps in order and drops links without a base URL", () => {
    const actions = networkExplorerActions({
      startStreaming: "/go-live",
      docsURL: "https://logbook.frameworks.network/",
      showcase: "https://frameworks.network/showcase",
    });
    expect(actions.map((a) => a.id)).toEqual(["start", "api", "methodology", "showcase"]);
    expect(actions[1].href).toBe(
      "https://logbook.frameworks.network/builders/analytics-api/#public-livepeer-observations"
    );
    expect(
      networkExplorerActions({ startStreaming: "/go-live", docsURL: "", showcase: "" }).map(
        (a) => a.id
      )
    ).toEqual(["start"]);
  });
});
