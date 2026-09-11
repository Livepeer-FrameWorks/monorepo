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

export function deferredReply() {
  let resolve!: (value: unknown) => void;
  const promise = new Promise<unknown>((yes) => {
    resolve = yes;
  });
  return { promise, resolve };
}
