<script lang="ts">
  import { getIconComponent } from "$lib/iconUtils";
  import { getStatusColor, getStatusIcon } from "$lib/utils/stream-helpers";

  let { stream, analytics } = $props();

  const statusIcon = $derived(getStatusIcon(stream?.status));
  const statusColor = $derived(getStatusColor(stream?.status));
  const StatusIconComponent = $derived(getIconComponent(statusIcon));
</script>

<div class="slab h-full shadow-none border-none">
  <div class="slab-header flex items-center justify-between">
    <h3 class="font-semibold text-xs uppercase tracking-wide text-muted-foreground">Stream Status</h3>
    <div class="h-6 flex items-center">
      <StatusIconComponent class="w-5 h-5 {statusColor}" />
    </div>
  </div>
  <div class="slab-body--padded space-y-2">
    <div class="flex justify-between items-center">
      <span class="text-sm text-muted-foreground">Status:</span>
      <span class="font-mono {statusColor} uppercase font-medium text-sm">
        {stream?.status || "Unknown"}
      </span>
    </div>
    <div class="flex justify-between items-center">
      <span class="text-sm text-muted-foreground">Recording:</span>
      <span
        class="font-mono {stream?.record
          ? 'text-success'
          : 'text-error'} font-medium text-sm"
      >
        {stream?.record ? "Enabled" : "Disabled"}
      </span>
    </div>
    {#if analytics?.currentViewers !== undefined}
      <div class="flex justify-between items-center">
        <span class="text-sm text-muted-foreground">Viewers:</span>
        <span class="font-mono text-info font-medium text-sm"
          >{analytics.currentViewers}</span
        >
      </div>
    {/if}
  </div>
</div>
