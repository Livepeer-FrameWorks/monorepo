export type ServiceInstanceLike = {
  id: string;
  instanceId: string;
  startedAt?: string | null;
  lastHealthCheck?: string | null;
};

export type NodePerformanceLike = {
  nodeId?: string | null;
  timestamp?: string | null;
};

const toTime = (value: string | null | undefined): number => {
  if (!value) return 0;
  const parsed = Date.parse(value);
  return Number.isNaN(parsed) ? 0 : parsed;
};

export function sortServiceInstancesForRender<T extends ServiceInstanceLike>(instances: T[]): T[] {
  return [...instances].sort((a, b) => {
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
  return samples.filter((sample) => sample.nodeId === nodeId);
}
