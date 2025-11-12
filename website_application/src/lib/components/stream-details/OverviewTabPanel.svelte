<script>
  import { formatDate, formatDuration } from "$lib/utils/stream-helpers";

  let { stream, streamKeys, recordings, analytics } = $props();
</script>

<div class="space-y-6">
  <div class="grid grid-cols-1 lg:grid-cols-2 gap-6">
    <!-- Stream Information -->
    <div
      class="p-6 rounded-lg bg-tokyo-night-bg-dark/50 border border-tokyo-night-fg-gutter/30 transition-all hover:border-tokyo-night-cyan/30"
    >
      <h4 class="text-lg font-semibold gradient-text mb-4">
        Stream Information
      </h4>
      <div class="space-y-3">
        <div>
          <span class="text-sm text-tokyo-night-comment">Name</span>
          <p class="text-tokyo-night-fg font-medium">
            {stream.name}
          </p>
        </div>
        {#if stream.description}
          <div>
            <span class="text-sm text-tokyo-night-comment">Description</span>
            <p class="text-tokyo-night-fg">{stream.description}</p>
          </div>
        {/if}
        <div>
          <span class="text-sm text-tokyo-night-comment">Created</span>
          <p class="text-tokyo-night-fg">
            {formatDate(stream.createdAt)}
          </p>
        </div>
        <div>
          <span class="text-sm text-tokyo-night-comment">Last Updated</span>
          <p class="text-tokyo-night-fg">
            {formatDate(stream.updatedAt)}
          </p>
        </div>
      </div>
    </div>

    <!-- Quick Stats -->
    <div
      class="p-6 rounded-lg bg-tokyo-night-bg-dark/50 border border-tokyo-night-fg-gutter/30 transition-all hover:border-tokyo-night-cyan/30"
    >
      <h4 class="text-lg font-semibold gradient-text mb-4">Quick Stats</h4>
      <div class="space-y-3">
        <div class="flex justify-between items-center">
          <span class="text-tokyo-night-comment">Total Stream Keys:</span>
          <span class="font-mono text-tokyo-night-cyan font-medium"
            >{streamKeys.length}</span
          >
        </div>
        <div class="flex justify-between items-center">
          <span class="text-tokyo-night-comment">Total Recordings:</span>
          <span class="font-mono text-tokyo-night-cyan font-medium"
            >{recordings.length}</span
          >
        </div>
        {#if analytics}
          <div class="flex justify-between items-center">
            <span class="text-tokyo-night-comment">24h Peak Viewers:</span>
            <span class="font-mono text-tokyo-night-cyan font-medium"
              >{analytics.peakViewers || 0}</span
            >
          </div>
          <div class="flex justify-between items-center">
            <span class="text-tokyo-night-comment">Total Watch Time:</span>
            <span class="font-mono text-tokyo-night-cyan font-medium"
              >{formatDuration(analytics.totalWatchTime || 0)}</span
            >
          </div>
        {/if}
      </div>
    </div>
  </div>
</div>
