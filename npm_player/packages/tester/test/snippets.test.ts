import { describe, expect, it } from "vitest";
import { DEFAULT_GATEWAY_URL } from "@livepeer-frameworks/player-core";
import { embedSnippet, SNIPPET_FRAMEWORKS } from "../src/snippets";

describe("embedSnippet", () => {
  it("omits the gateway URL when it is the official one", () => {
    for (const framework of SNIPPET_FRAMEWORKS) {
      const code = embedSnippet(framework, { contentId: "abc", gatewayUrl: DEFAULT_GATEWAY_URL });
      expect(code).toContain('"abc"');
      expect(code).not.toMatch(/gateway/i);
    }
  });

  it("writes Mist options as props, options or attributes per framework", () => {
    const input = { contentId: "live", mistUrl: "https://mist.example.com", forceType: "whep" };
    expect(embedSnippet("React", input)).toContain('mistUrl: "https://mist.example.com"');
    expect(embedSnippet("Svelte", input)).toContain("@livepeer-frameworks/player-svelte");
    expect(embedSnippet("Web Component", input)).toContain('mist-url="https://mist.example.com"');
    expect(embedSnippet("Web Component", input)).toContain('force-type="whep"');
    expect(embedSnippet("JavaScript", input)).toContain('mistUrl: "https://mist.example.com"');
  });

  it("carries pre-resolved endpoints for a direct URL", () => {
    const url = "https://cdn.example.com/live/index.m3u8";
    const input = {
      contentId: "pasted-url",
      endpoints: {
        primary: {
          nodeId: "pasted-url",
          protocol: "HLS",
          url,
          outputs: { HLS: { protocol: "HLS", url } },
        },
        fallbacks: [],
      },
    };
    for (const framework of SNIPPET_FRAMEWORKS) {
      expect(embedSnippet(framework, input)).toContain(url);
    }
    expect(embedSnippet("React", input)).not.toContain("contentType");
  });
});
