export function viewerReply(protocol: "HLS" | "DASH", node = protocol.toLowerCase()) {
  const url = `https://${node}.example/prepared?receipt=${node}`;
  return {
    data: {
      resolveViewerEndpoint: {
        primary: {
          nodeId: node,
          baseUrl: `https://${node}.example`,
          protocol: protocol.toLowerCase(),
          url,
          outputs: JSON.stringify({
            [protocol]: { url },
            WHEP: { url: "https://unprepared.example/whep" },
          }),
        },
        fallbacks: [{ nodeId: "unprepared", url: "https://unprepared.example" }],
        metadata: {
          contentId: "live+internal",
          thumbnailAssets: { posterUrl: "https://poster.example/image.jpg" },
        },
      },
    },
  };
}

const SUPPORTED_SERVER_INFO = {
  data: { serverInfo: { version: "v0.3.11", features: ["playback", "viewer-protocol-selection"] } },
};

/**
 * A fetch for a gateway this player supports: the serverInfo probe is
 * answered here and every other request goes to fetcher, so fetcher's calls
 * are the resolves alone.
 */
export function supportedGateway(fetcher: (url: string, init?: RequestInit) => unknown) {
  return async (url: string, init?: RequestInit) => {
    if (typeof init?.body === "string" && init.body.includes("serverInfo")) {
      return { ok: true, json: async () => SUPPORTED_SERVER_INFO };
    }
    return fetcher(url, init);
  };
}

export function deferredReply() {
  let resolve!: (value: unknown) => void;
  const promise = new Promise<unknown>((yes) => {
    resolve = yes;
  });
  return { promise, resolve };
}
