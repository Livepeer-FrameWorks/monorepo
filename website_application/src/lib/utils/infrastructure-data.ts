export type ServiceInstanceLike = {
  id: string;
  instanceId: string;
  status?: string | null;
  startedAt?: string | null;
  lastHealthCheck?: string | null;
};

export type NodePerformanceLike = {
  id?: string | null;
  nodeId?: string | null;
  timestamp?: string | null;
};

const toTime = (value: string | null | undefined): number => {
  if (!value) return 0;
  const parsed = Date.parse(value);
  return Number.isNaN(parsed) ? 0 : parsed;
};

export function sortServiceInstancesForRender<T extends ServiceInstanceLike>(instances: T[]): T[] {
  const activeInstances = instances.filter((instance) => {
    const status = instance.status?.toLowerCase() ?? "";
    return status === "" || status === "running" || status === "starting" || status === "active";
  });

  return [...activeInstances].sort((a, b) => {
    const healthDelta = toTime(b.lastHealthCheck) - toTime(a.lastHealthCheck);
    if (healthDelta !== 0) return healthDelta;

    const startDelta = toTime(b.startedAt) - toTime(a.startedAt);
    if (startDelta !== 0) return startDelta;

    return a.instanceId.localeCompare(b.instanceId);
  });
}

export function serviceInstanceRenderKey(instance: ServiceInstanceLike): string {
  return `${instance.instanceId}:${instance.startedAt ?? "unknown"}:${instance.id}`;
}

export function filterNodePerformance<T extends NodePerformanceLike>(
  samples: T[],
  nodeId: string
): T[] {
  if (!nodeId) return [];

  const deduped = new Map<string, T>();
  for (const sample of samples) {
    if (sample.nodeId !== nodeId) continue;
    const key = sample.id ?? `${sample.nodeId ?? "unknown"}:${sample.timestamp ?? "unknown"}`;
    if (!deduped.has(key)) {
      deduped.set(key, sample);
    }
  }

  return [...deduped.values()].sort((a, b) => {
    const timeDelta = toTime(b.timestamp) - toTime(a.timestamp);
    if (timeDelta !== 0) return timeDelta;
    return (a.id ?? "").localeCompare(b.id ?? "");
  });
}
