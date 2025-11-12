<script lang="ts">
  import { Card, CardContent } from "$lib/components/ui/card";
  import { Badge } from "$lib/components/ui/badge";

  interface Node {
    id: string;
    name: string;
    type: string;
    region: string;
    ipAddress?: string;
    lastSeen: string;
  }

  interface SystemHealthData {
    event: {
      status: string;
      healthScore?: number;
      diskUsage?: number;
    };
    ts: Date;
  }

  interface Props {
    node: Node;
    systemHealth: Record<string, SystemHealthData>;
    getNodeStatus: (nodeId: string) => string;
    getNodeHealthScore: (nodeId: string) => number;
    formatCpuUsage: (nodeId: string) => string;
    formatMemoryUsage: (nodeId: string) => string;
    getStatusBadgeClass: (status: string | null | undefined) => string;
  }

  let {
    node,
    systemHealth,
    getNodeStatus,
    getNodeHealthScore,
    formatCpuUsage,
    formatMemoryUsage,
    getStatusBadgeClass,
  }: Props = $props();
</script>

<Card>
  <CardContent class="space-y-4">
    <div class="flex items-start justify-between gap-3">
      <div>
        <h3 class="text-lg font-semibold">{node.name}</h3>
        <p class="text-sm text-tokyo-night-comment">
          {node.type} • {node.region}
        </p>
      </div>
      <div class="text-right space-y-1">
        <Badge
          variant="outline"
          tone={getNodeHealthScore(node.id) >= 80 ? 'green' :
                getNodeHealthScore(node.id) >= 50 ? 'yellow' :
                getNodeHealthScore(node.id) > 0 ? 'red' :
                'neutral'}
          class="text-xs uppercase"
        >
          {getNodeStatus(node.id)}
        </Badge>
        {#if systemHealth[node.id]}
          <p class="text-xs text-tokyo-night-comment">
            Health: {getNodeHealthScore(node.id)}%
          </p>
        {/if}
      </div>
    </div>

    <div class="grid grid-cols-2 gap-4 text-sm">
      <div>
        <p class="text-tokyo-night-comment">CPU Usage</p>
        <p class="font-medium">{formatCpuUsage(node.id)}</p>
      </div>
      <div>
        <p class="text-tokyo-night-comment">Memory Usage</p>
        <p class="font-medium">{formatMemoryUsage(node.id)}</p>
      </div>
      <div>
        <p class="text-tokyo-night-comment">IP Address</p>
        <p class="font-mono text-xs">
          {node.ipAddress || "N/A"}
        </p>
      </div>
      <div>
        <p class="text-tokyo-night-comment">Last Seen</p>
        <p class="text-xs">
          {new Date(node.lastSeen).toLocaleString()}
        </p>
      </div>
    </div>

    {#if systemHealth[node.id]}
      <div
        class="grid grid-cols-3 gap-2 border-t border-border/40 pt-3 text-xs"
      >
        <div>
          <p class="text-tokyo-night-comment">Disk</p>
          <p>
            {Math.round(systemHealth[node.id].event.diskUsage || 0)}%
          </p>
        </div>
        <div>
          <p class="text-tokyo-night-comment">Updated</p>
          <p>
            {systemHealth[node.id].ts.toLocaleTimeString()}
          </p>
        </div>
        <div>
          <p class="text-tokyo-night-comment">Score</p>
          <p>{getNodeHealthScore(node.id)}%</p>
        </div>
      </div>
    {/if}
  </CardContent>
</Card>
