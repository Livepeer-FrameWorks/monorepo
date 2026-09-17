import type { PullSourcePlacementClass } from "$lib/utils/pull-source";

export type SourceLocationMode = "ANY" | "RESTRICTED" | "CUSTOM";

export interface SourceLocationClusterValue {
  clusterId: string;
  nodeIds: string[];
}

export interface SourceLocationValue {
  mode: SourceLocationMode;
  clusters: readonly { clusterId: string; nodeIds: readonly string[] }[];
  avoidNodeIds: readonly string[];
}

/** Editable source location. CUSTOM is never editable here. */
export interface SourceLocationDraft {
  mode: "ANY" | "RESTRICTED";
  clusters: SourceLocationClusterValue[];
  avoidNodeIds: string[];
}

export interface SourceLocationClusterChoice {
  clusterId: string;
  clusterName: string;
  accessLevel: string;
  allowPrivatePullSources: boolean;
}

export interface SourceLocationNodeOption {
  id: string;
  name: string;
}

export function anySourceLocation(): SourceLocationDraft {
  return { mode: "ANY", clusters: [], avoidNodeIds: [] };
}

/** Returns null for CUSTOM locations, which only the advanced placement editor can change. */
export function draftFromSourceLocation(
  location: SourceLocationValue | null | undefined
): SourceLocationDraft | null {
  if (!location || location.mode === "ANY") return anySourceLocation();
  if (location.mode === "CUSTOM") return null;
  return {
    mode: "RESTRICTED",
    clusters: location.clusters.map((cluster) => ({
      clusterId: cluster.clusterId,
      nodeIds: [...cluster.nodeIds],
    })),
    avoidNodeIds: [...location.avoidNodeIds],
  };
}

export function sourceLocationInput(draft: SourceLocationDraft): SourceLocationDraft {
  if (draft.mode === "ANY") return anySourceLocation();
  return {
    mode: "RESTRICTED",
    clusters: draft.clusters.map((cluster) => ({
      clusterId: cluster.clusterId,
      nodeIds: [...new Set(cluster.nodeIds)],
    })),
    avoidNodeIds: [...new Set(draft.avoidNodeIds)],
  };
}

function canonical(draft: SourceLocationDraft): string {
  const input = sourceLocationInput(draft);
  return JSON.stringify({
    mode: input.mode,
    clusters: input.clusters
      .map((cluster) => ({ clusterId: cluster.clusterId, nodeIds: [...cluster.nodeIds].sort() }))
      .sort((a, b) => a.clusterId.localeCompare(b.clusterId)),
    avoidNodeIds: [...input.avoidNodeIds].sort(),
  });
}

export function sameSourceLocation(a: SourceLocationDraft, b: SourceLocationDraft): boolean {
  return canonical(a) === canonical(b);
}

export function isOwnedCluster(cluster: Pick<SourceLocationClusterChoice, "accessLevel">): boolean {
  return cluster.accessLevel === "owner";
}

/**
 * Clusters offered for a source. Private and multicast sources are only
 * offered clusters whose owner allows private pull sources; the server applies
 * the same rule on save.
 */
export function offeredClusters(
  clusters: readonly SourceLocationClusterChoice[],
  sourceClass: PullSourcePlacementClass
): { offered: SourceLocationClusterChoice[]; hidden: number } {
  if (sourceClass === "public") return { offered: [...clusters], hidden: 0 };
  const offered = clusters.filter((cluster) => cluster.allowPrivatePullSources);
  return { offered, hidden: clusters.length - offered.length };
}

/** A blocking problem the form can detect before the server rejects the change. */
export function sourceLocationProblem(
  draft: SourceLocationDraft,
  sourceClass: PullSourcePlacementClass,
  clusters: readonly SourceLocationClusterChoice[]
): string | null {
  if (draft.mode === "ANY") {
    return sourceClass === "private"
      ? "Private and multicast sources need specific clusters. Choose Only these clusters."
      : null;
  }
  if (draft.clusters.length === 0) return "Choose at least one cluster.";
  if (sourceClass === "private") {
    for (const selected of draft.clusters) {
      const cluster = clusters.find((item) => item.clusterId === selected.clusterId);
      if (cluster && !cluster.allowPrivatePullSources) {
        return `${cluster.clusterName} does not allow private pull sources.`;
      }
    }
  }
  return null;
}

export function describeSourceLocation(
  location: SourceLocationValue | null | undefined,
  clusterName: (clusterId: string) => string = (clusterId) => clusterId
): string {
  if (!location || location.mode === "ANY") return "Any cluster FrameWorks chooses";
  if (location.mode === "CUSTOM") return "Custom placement rules";
  const clusters = location.clusters.map((cluster) =>
    cluster.nodeIds.length
      ? `${clusterName(cluster.clusterId)} (${cluster.nodeIds.length} ${cluster.nodeIds.length === 1 ? "node" : "nodes"})`
      : clusterName(cluster.clusterId)
  );
  const avoided = location.avoidNodeIds.length
    ? `, avoiding ${location.avoidNodeIds.length} ${location.avoidNodeIds.length === 1 ? "node" : "nodes"}`
    : "";
  return `Only ${clusters.join(", ")}${avoided}`;
}
