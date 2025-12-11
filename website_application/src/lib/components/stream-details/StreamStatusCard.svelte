<script lang="ts">
  import { getIconComponent } from "$lib/iconUtils";
  import { getStatusColor, getStatusIcon } from "$lib/utils/stream-helpers";

  let { stream, analytics } = $props();

  const statusIcon = $derived(getStatusIcon(stream?.status));
  const statusColor = $derived(getStatusColor(stream?.status));
  const StatusIconComponent = $derived(getIconComponent(statusIcon));
</script>

<div class="p-6">
  <div class="flex items-center justify-between mb-4">
    <h3 class="text-lg font-semibold gradient-text">Stream Status</h3>
    <StatusIconComponent class="w-6 h-6 {statusColor}" />
  </div>
  <div class="space-y-2">
    <div class="flex justify-between items-center">
      <span class="text-muted-foreground">Status:</span>
      <span class="font-mono {statusColor} uppercase font-medium">
        {stream?.status || "Unknown"}
      </span>
    </div>
    <div class="flex justify-between items-center">
      <span class="text-muted-foreground">Recording:</span>
      <span
        class="font-mono {stream?.record
          ? 'text-success'
          : 'text-error'} font-medium"
      >
        {stream?.record ? "Enabled" : "Disabled"}
      </span>
    </div>
    {#if analytics?.currentViewers !== undefined}
      <div class="flex justify-between items-center">
        <span class="text-muted-foreground">Viewers:</span>
        <span class="font-mono text-info font-medium"
          >{analytics.currentViewers}</span
        >
      </div>
    {/if}
  </div>
</div>
