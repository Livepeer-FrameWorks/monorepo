<script lang="ts">
  import { formatBytes, formatDate, formatNumber } from "$lib/utils/formatters.js";
  import { resolve } from "$app/paths";
  import { Badge } from "$lib/components/ui/badge";
  interface Props {
    stream: StreamDetails;
    compact?: boolean;
    actions?: import("svelte").Snippet<[StreamDetails]>;
  }

  interface StreamDetails {
    streamName?: string;
    internalName?: string;
    id?: string;
    status?: "live" | "offline" | "error" | string;
    currentViewers?: number;
    peakViewers?: number;
    bandwidthOut?: number;
    totalConnections?: number;
    resolution?: string;
    bitrateKbps?: number;
    location?: string;
    lastUpdated?: string | number | Date;
  }

  let { stream, compact = false, actions }: Props = $props();
</script>

<div class="bg-slate-800 border border-slate-700 rounded-lg p-6 hover:border-slate-600 transition-colors">
  <!-- Header -->
  <div class="flex items-start justify-between mb-4">
    <div class="flex-1 min-w-0">
      <h3 class="text-lg font-semibold text-slate-100 truncate">
        {stream.streamName || stream.internalName || 'Unnamed Stream'}
      </h3>
      <div class="text-sm text-slate-400 font-mono mt-1">
        {stream.id || stream.internalName}
      </div>
    </div>
    
    <!-- Status Badge -->
    <div class="ml-4">
      {#if stream.status}
        <Badge
          variant="outline"
          tone={stream.status === 'live' ? 'green' :
                stream.status === 'error' ? 'red' :
                'neutral'}
        >
          <span class="w-2 h-2 rounded-full mr-1.5 {
            stream.status === 'live' ? 'bg-green-400' :
            stream.status === 'offline' ? 'bg-gray-400' :
            stream.status === 'error' ? 'bg-red-400' :
            'bg-slate-400'
          }"></span>
          {stream.status}
        </Badge>
      {/if}
    </div>
  </div>

  {#if !compact}
    <!-- Metrics Grid -->
    <div class="grid grid-cols-2 md:grid-cols-4 gap-4 mb-4">
      <div class="text-center">
        <div class="text-xl font-bold text-slate-100">
          {formatNumber(stream.currentViewers || 0)}
        </div>
        <div class="text-xs text-slate-400">Current Viewers</div>
      </div>
      
      <div class="text-center">
        <div class="text-xl font-bold text-slate-100">
          {formatNumber(stream.peakViewers || 0)}
        </div>
        <div class="text-xs text-slate-400">Peak Viewers</div>
      </div>
      
      <div class="text-center">
        <div class="text-xl font-bold text-slate-100">
          {formatBytes(stream.bandwidthOut || 0)}
        </div>
        <div class="text-xs text-slate-400">Bandwidth Out</div>
      </div>
      
      <div class="text-center">
        <div class="text-xl font-bold text-slate-100">
          {formatNumber(stream.totalConnections || 0)}
        </div>
        <div class="text-xs text-slate-400">Total Connections</div>
      </div>
    </div>

    <!-- Additional Details -->
    <div class="border-t border-slate-700 pt-4">
      <div class="grid grid-cols-1 md:grid-cols-3 gap-4 text-sm">
        {#if stream.resolution}
          <div>
            <span class="text-slate-400">Resolution:</span>
            <span class="text-slate-100 ml-2">{stream.resolution}</span>
          </div>
        {/if}
        
        {#if stream.bitrateKbps}
          <div>
            <span class="text-slate-400">Bitrate:</span>
            <span class="text-slate-100 ml-2">{stream.bitrateKbps} kbps</span>
          </div>
        {/if}
        
        {#if stream.location}
          <div>
            <span class="text-slate-400">Location:</span>
            <span class="text-slate-100 ml-2">{stream.location}</span>
          </div>
        {/if}
      </div>
      
      {#if stream.lastUpdated}
        <div class="mt-3 text-xs text-slate-500">
          Last updated: {formatDate(stream.lastUpdated)}
        </div>
      {/if}
    </div>
  {:else}
    <!-- Compact view -->
    <div class="flex items-center justify-between text-sm">
      <div class="flex space-x-4">
        <span class="text-slate-400">
          Viewers: <span class="text-slate-100">{formatNumber(stream.currentViewers || 0)}</span>
        </span>
        {#if stream.resolution}
          <span class="text-slate-400">
            Quality: <span class="text-slate-100">{stream.resolution}</span>
          </span>
        {/if}
      </div>
      
      {#if stream.lastUpdated}
        <div class="text-slate-500">
          {formatDate(stream.lastUpdated)}
        </div>
      {/if}
    </div>
  {/if}

  <!-- Actions -->
  <div class="flex items-center justify-end mt-4 space-x-2">
    {@render actions?.({ stream, })}
    
    {#if stream.id}
      <a
        href={resolve(`/streams/${stream.id}`)}
        class="text-blue-400 hover:text-blue-300 text-sm font-medium transition-colors"
      >
        View Details
      </a>
    {/if}
  </div>
</div>
