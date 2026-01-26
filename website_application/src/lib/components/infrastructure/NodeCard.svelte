<script lang="ts">
  import { Card, CardContent } from "$lib/components/ui/card";
  import { Badge } from "$lib/components/ui/badge";
  import type { NodeStatus$options } from "$houdini";

  // Node data type - fields needed by this component
  interface NodeCardData {
    id: string;
    nodeName: string;
    nodeType: string;
    region: string | null;
    externalIp: string | null;
    internalIp: string | null;
    wireguardIp: string | null;
    lastHeartbeat: string | null;
    cpuCores: number | null;
    memoryGb: number | null;
    diskGb: number | null;
    availabilityZone: string | null;
  }

  // Real-time system health data structure (includes ts which is computed client-side)
  interface SystemHealthData {
    event: {
      diskTotalBytes: number | null;
      diskUsedBytes: number | null;
      shmTotalBytes: number | null;
      shmUsedBytes: number | null;
      status: NodeStatus$options;
    };
    ts: Date;
  }

  interface Props {
    node: NodeCardData;
    systemHealth: Record<string, SystemHealthData>;
    getNodeStatus: (nodeId: string) => string;
    getNodeHealthScore: (nodeId: string) => number;
    formatCpuUsage: (nodeId: string) => string;
    formatMemoryUsage: (nodeId: string) => string;
    formatDiskUsage: (nodeId: string) => string;
  }

  let {
    node,
    systemHealth,
    getNodeStatus,
    getNodeHealthScore,
    formatCpuUsage,
    formatMemoryUsage,
    formatDiskUsage,
  }: Props = $props();
</script>

<Card>
  <CardContent class="space-y-4">
    <div class="flex items-start justify-between gap-3">
      <div>
        <h3 class="text-lg font-semibold">{node.nodeName}</h3>
        <p class="text-sm text-muted-foreground">
          {node.nodeType} • {node.region}
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
          <p class="text-xs text-muted-foreground">
            Health: {getNodeHealthScore(node.id)}%
          </p>
        {/if}
      </div>
    </div>

    <!-- Resource Usage -->
    <div class="grid grid-cols-3 gap-2 text-sm min-h-[44px]">
      {#if systemHealth[node.id]}
        <div>
          <p class="text-muted-foreground">CPU</p>
          <p class="font-medium">{formatCpuUsage(node.id)}</p>
        </div>
        <div>
          <p class="text-muted-foreground">Memory</p>
          <p class="font-medium">{formatMemoryUsage(node.id)}</p>
        </div>
        <div>
          <p class="text-muted-foreground">Disk</p>
          <p class="font-medium">{formatDiskUsage(node.id)}</p>
        </div>
      {:else}
        <div class="col-span-3 flex items-center justify-center text-xs text-muted-foreground animate-pulse">
          Waiting for live stats...
        </div>
      {/if}
    </div>

    <!-- Capacity Specs -->
    {#if node.cpuCores || node.memoryGb || node.diskGb}
      <div class="grid grid-cols-3 gap-2 text-xs border-t border-border/40 pt-3">
        {#if node.cpuCores}
          <div>
            <p class="text-muted-foreground">CPU Cores</p>
            <p class="font-mono text-primary">{node.cpuCores}</p>
          </div>
        {/if}
        {#if node.memoryGb}
          <div>
            <p class="text-muted-foreground">Memory</p>
            <p class="font-mono text-accent-purple">{node.memoryGb} GB</p>
          </div>
        {/if}
        {#if node.diskGb}
          <div>
            <p class="text-muted-foreground">Disk</p>
            <p class="font-mono text-info">{node.diskGb} GB</p>
          </div>
        {/if}
      </div>
    {/if}

    <!-- Network Info -->
    <div class="grid grid-cols-2 gap-4 text-sm border-t border-border/40 pt-3">
      {#if node.externalIp}
        <div>
          <p class="text-muted-foreground">External IP</p>
          <p class="font-mono text-xs">
            {node.externalIp}
          </p>
        </div>
      {/if}
      {#if node.internalIp}
        <div>
          <p class="text-muted-foreground">Internal IP</p>
          <p class="font-mono text-xs">{node.internalIp}</p>
        </div>
      {/if}
      {#if node.wireguardIp}
        <div>
          <p class="text-muted-foreground">WireGuard IP</p>
          <p class="font-mono text-xs">{node.wireguardIp}</p>
        </div>
      {/if}
      {#if node.availabilityZone}
        <div>
          <p class="text-muted-foreground">AZ</p>
          <p class="text-xs">{node.availabilityZone}</p>
        </div>
      {/if}
      <div>
        <p class="text-muted-foreground">Last Seen</p>
        <p class="text-xs">
          {#if systemHealth[node.id]}
            {systemHealth[node.id].ts.toLocaleString()}
          {:else if node.lastHeartbeat}
            {new Date(node.lastHeartbeat).toLocaleString()}
          {:else}
            N/A
          {/if}
        </p>
      </div>
    </div>

    {#if systemHealth[node.id]}
      {@const shmUsed = systemHealth[node.id].event.shmUsedBytes || 0}
      {@const shmTotal = systemHealth[node.id].event.shmTotalBytes || 1}
      {@const shmPercent = (shmUsed / shmTotal) * 100}
      <div
        class="grid grid-cols-2 md:grid-cols-4 gap-2 border-t border-border/40 pt-3 text-xs"
      >
        <div>
          <p class="text-muted-foreground">Disk</p>
          <p>
            {Math.round((systemHealth[node.id].event.diskUsedBytes || 0) / (systemHealth[node.id].event.diskTotalBytes || 1) * 100)}%
          </p>
        </div>
        <div>
          <p class="text-muted-foreground">SHM</p>
          <p>
            {#if shmPercent > 0 && shmPercent < 1}
              &lt; 1%
            {:else}
              {Math.round(shmPercent)}%
            {/if}
          </p>
        </div>
        <div>
          <p class="text-muted-foreground">Updated</p>
          <p>
            {systemHealth[node.id].ts.toLocaleTimeString()}
          </p>
        </div>
        <div>
          <p class="text-muted-foreground">Score</p>
          <p>{getNodeHealthScore(node.id)}%</p>
        </div>
      </div>
    {/if}
  </CardContent>
</Card>
