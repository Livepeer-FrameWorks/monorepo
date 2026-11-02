import type { Map as MapLibreMap } from "maplibre-gl";

export type NetworkView = "frameworks" | "livepeer";
export type ExplorerViewport = {
  camera?: { center: [number, number]; zoom: number; bearing: number; pitch: number };
};

// networkAPIURL resolves a Bridge REST path against the configured GraphQL endpoint. That endpoint is relative
// ("/graphql") when the page and Bridge share an origin through a proxy, so it resolves against the page origin first.
export function networkAPIURL(path: string, graphqlURL: string, origin: string): string {
  return new URL(path, new URL(graphqlURL || "/", origin)).toString();
}

// livepeerOrchestratorURL is the Livepeer Explorer page of an orchestrator's onchain address, or undefined when the
// identity is not an Ethereum address.
export function livepeerOrchestratorURL(explorerURL: string, address: string): string | undefined {
  if (!/^0x[0-9a-fA-F]{40}$/.test(address)) return undefined;
  return new URL(`/accounts/${address.toLowerCase()}/orchestrating`, explorerURL).toString();
}

export type ExplorerAction = { id: string; label: string; href: string };

// networkExplorerActions is the row of next steps under the explorer, shared by the marketing site and the dashboard
// so both offer the same paths in the same order. A link whose base URL the deployment does not set is left out.
export function networkExplorerActions(links: {
  startStreaming: string;
  docsURL: string;
  showcase: string;
}): ExplorerAction[] {
  const docs = links.docsURL.replace(/\/+$/, "");
  const actions: ExplorerAction[] = [
    { id: "start", label: "Start streaming", href: links.startStreaming },
  ];
  if (docs)
    actions.push(
      {
        id: "api",
        label: "Network API",
        href: `${docs}/builders/analytics-api/#public-livepeer-observations`,
      },
      {
        id: "methodology",
        label: "How observations work",
        href: `${docs}/operators/network-observations/`,
      }
    );
  if (links.showcase) actions.push({ id: "showcase", label: "Showcase", href: links.showcase });
  return actions;
}

// ObservationsFailure is a failed observations request. notConfigured marks a deployment without a network observer,
// which Bridge answers with 503 and an error naming the missing configuration; a retry cannot fix it.
export type ObservationsFailure = { message: string; notConfigured: boolean };

export function observationsFailure(status: number, body: unknown): ObservationsFailure {
  const error =
    typeof body === "object" &&
    body !== null &&
    typeof (body as { error?: unknown }).error === "string"
      ? (body as { error: string }).error
      : "";
  if (status === 503 && /not configured/i.test(error))
    return {
      message: "Network observations are not set up on this deployment.",
      notConfigured: true,
    };
  return { message: `Observations unavailable (HTTP ${status})`, notConfigured: false };
}

export function networkView(search: URLSearchParams): NetworkView {
  return search.get("view") === "livepeer" ? "livepeer" : "frameworks";
}

export function networkViewURL(
  search: URLSearchParams,
  view: NetworkView
): "/network" | `/network?${string}` {
  const next = new URLSearchParams(search);
  if (view === "livepeer") next.set("view", view);
  else next.delete("view");
  return next.size ? `/network?${next}` : "/network";
}

export function networkTabKey(view: NetworkView, key: string): NetworkView | undefined {
  if (key === "Home") return "frameworks";
  if (key === "End") return "livepeer";
  if (key === "ArrowLeft" || key === "ArrowRight")
    return view === "frameworks" ? "livepeer" : "frameworks";
}

export function bindExplorerViewport(map: MapLibreMap, viewport?: ExplorerViewport): () => void {
  if (!viewport) return () => {};
  const capture = () => {
    const center = map.getCenter();
    viewport.camera = {
      center: [center.lng, center.lat],
      zoom: map.getZoom(),
      bearing: map.getBearing(),
      pitch: map.getPitch(),
    };
  };
  map.on("moveend", capture);
  return () => {
    capture();
    map.off("moveend", capture);
  };
}
