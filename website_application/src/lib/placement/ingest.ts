import type { ResolveIngestDestination$result } from "$houdini";

export type ResolvedIngest = NonNullable<ResolveIngestDestination$result["resolveIngestEndpoint"]>;
export type IngestDestination = ResolvedIngest["primary"];

export function destinationHost(destination: IngestDestination): string | null {
  try {
    const url = new URL(destination.baseUrl);
    if (!["http:", "https:"].includes(url.protocol) || url.username || url.password) return null;
    return url.host;
  } catch {
    return null;
  }
}

export function isSpecificIngest(destination: IngestDestination): boolean {
  return (
    destination.kind === "INGEST_ENDPOINT_KIND_NODE_SPECIFIC" &&
    !!destination.nodeId &&
    !!destination.clusterId &&
    !!destinationHost(destination)
  );
}

export function ingestProtocolUrl(
  destination: IngestDestination,
  protocol: "rtmp" | "srt" | "whip"
): string | null {
  const value =
    destination[protocol === "rtmp" ? "rtmpUrl" : protocol === "srt" ? "srtUrl" : "whipUrl"];
  if (!value) return null;
  try {
    const url = new URL(value);
    const allowed =
      protocol === "whip"
        ? ["http:", "https:"]
        : protocol === "rtmp"
          ? ["rtmp:", "rtmps:"]
          : ["srt:"];
    if (
      !allowed.includes(url.protocol) ||
      !url.hostname ||
      url.username ||
      url.password ||
      url.hash
    )
      return null;
    return value;
  } catch {
    return null;
  }
}

// Only split the exact credential echoed in the terminal path segment. Other
// server-specific URL shapes must stay intact instead of inventing an OBS app/key.
export function splitRtmpDestination(
  destination: IngestDestination,
  streamKey: string
): { server: string; key: string } | null {
  const value = ingestProtocolUrl(destination, "rtmp");
  if (!value || !streamKey) return null;
  try {
    const url = new URL(value);
    if (url.search) return null;
    const slash = url.pathname.lastIndexOf("/");
    if (slash < 0 || decodeURIComponent(url.pathname.slice(slash + 1)) !== streamKey) return null;
    url.pathname = url.pathname.slice(0, slash);
    return { server: url.toString(), key: streamKey };
  } catch {
    return null;
  }
}
