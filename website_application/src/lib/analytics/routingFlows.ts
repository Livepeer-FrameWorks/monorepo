export type RoutingFlowEvent = {
  clientBucket?: { h3Index?: string | null } | null;
  nodeBucket?: { h3Index?: string | null } | null;
  routingDistance?: number | null;
  remoteClusterId?: string | null;
  clusterId?: string | null;
  status?: string | null;
};

export type BucketFlow = {
  from: string;
  to: string;
  count: number;
  distanceSum: number;
  crossCluster: boolean;
  avgDistance: number;
};

export function isCrossClusterEvent(evt: RoutingFlowEvent): boolean {
  if (evt.remoteClusterId && evt.remoteClusterId !== evt.clusterId) {
    return true;
  }

  return evt.status === "remote_redirect" || evt.status === "cross_cluster_dtsc";
}

export function buildBucketFlows(events: RoutingFlowEvent[]): BucketFlow[] {
  const flows: Record<
    string,
    { from: string; to: string; count: number; distanceSum: number; crossCluster: boolean }
  > = {};

  for (const evt of events) {
    const from = evt.clientBucket?.h3Index;
    const to = evt.nodeBucket?.h3Index;
    if (!from || !to) continue;

    const crossCluster = isCrossClusterEvent(evt);
    const key = `${from}->${to}->${crossCluster ? "cross" : "local"}`;

    if (!flows[key]) {
      flows[key] = { from, to, count: 0, distanceSum: 0, crossCluster };
    }

    flows[key].count += 1;
    flows[key].distanceSum += evt.routingDistance ?? 0;
  }

  return Object.values(flows)
    .map((f) => ({
      ...f,
      avgDistance: f.count ? f.distanceSum / f.count : 0,
    }))
    .sort((a, b) => b.count - a.count);
}
