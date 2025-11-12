<script>
  import { getIconComponent } from "$lib/iconUtils";
  import { getStatusColor, getStatusIcon } from "$lib/utils/stream-helpers";

  let { stream, analytics } = $props();

  const statusIcon = $derived(getStatusIcon(stream?.status));
  const statusColor = $derived(getStatusColor(stream?.status));
  const StatusIconComponent = $derived(getIconComponent(statusIcon));
</script>

<div
  class="glass-card p-6 transition-all hover:shadow-brand-subtle hover:scale-[1.01]"
>
  <div class="flex items-center justify-between mb-4">
    <h3 class="text-lg font-semibold gradient-text">Stream Status</h3>
    <StatusIconComponent class="w-6 h-6 {statusColor}" />
  </div>
  <div class="space-y-2">
    <div class="flex justify-between items-center">
      <span class="text-tokyo-night-comment">Status:</span>
      <span class="font-mono {statusColor} uppercase font-medium">
        {stream?.status || "Unknown"}
      </span>
    </div>
    <div class="flex justify-between items-center">
      <span class="text-tokyo-night-comment">Recording:</span>
      <span
        class="font-mono {stream?.record
          ? 'text-green-400'
          : 'text-red-400'} font-medium"
      >
        {stream?.record ? "Enabled" : "Disabled"}
      </span>
    </div>
    {#if analytics?.currentViewers !== undefined}
      <div class="flex justify-between items-center">
        <span class="text-tokyo-night-comment">Viewers:</span>
        <span class="font-mono text-tokyo-night-cyan font-medium"
          >{analytics.currentViewers}</span
        >
      </div>
    {/if}
  </div>
</div>
