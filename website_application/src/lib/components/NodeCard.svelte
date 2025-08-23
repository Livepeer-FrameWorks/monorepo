<script>
  import { formatBytes, formatDate, formatUptime, formatPercentage } from '$lib/utils/formatters.js';
  export let node;
  export let compact = false;
</script>

<div class="bg-slate-800 border border-slate-700 rounded-lg p-6 hover:border-slate-600 transition-colors">
  <!-- Header -->
  <div class="flex items-start justify-between mb-4">
    <div class="flex-1 min-w-0">
      <h3 class="text-lg font-semibold text-slate-100 truncate">
        {node.name || node.nodeId || 'Unnamed Node'}
      </h3>
      <div class="text-sm text-slate-400 font-mono mt-1">
        {node.nodeId}
      </div>
      {#if node.region || node.location}
        <div class="text-sm text-slate-500 mt-1">
          {node.location}{node.region ? ` (${node.region})` : ''}
        </div>
      {/if}
    </div>
    
    <!-- Health Status Badge -->
    <div class="ml-4">
      {#if node.status}
        <span class="inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium {
          node.status === 'online' || node.status === 'healthy' ? 'bg-green-900/30 text-green-400 border border-green-500/30' :
          node.status === 'offline' || node.status === 'unhealthy' ? 'bg-red-900/30 text-red-400 border border-red-500/30' :
          node.status === 'degraded' || node.status === 'warning' ? 'bg-yellow-900/30 text-yellow-400 border border-yellow-500/30' :
          'bg-slate-700 text-slate-300'
        }">
          <span class="w-2 h-2 rounded-full mr-1.5 {
            node.status === 'online' || node.status === 'healthy' ? 'bg-green-400' :
            node.status === 'offline' || node.status === 'unhealthy' ? 'bg-red-400' :
            node.status === 'degraded' || node.status === 'warning' ? 'bg-yellow-400' :
            'bg-slate-400'
          }"></span>
          {node.status}
        </span>
      {/if}
    </div>
  </div>

  {#if !compact}
    <!-- Metrics Grid -->
    <div class="grid grid-cols-2 md:grid-cols-4 gap-4 mb-4">
      <div class="text-center">
        <div class="text-xl font-bold text-slate-100">
          {node.cpuUsage !== undefined ? node.cpuUsage.toFixed(1) + '%' : 'N/A'}
        </div>
        <div class="text-xs text-slate-400">CPU Usage</div>
      </div>
      
      <div class="text-center">
        <div class="text-xl font-bold text-slate-100">
          {node.memoryUsage !== undefined ? node.memoryUsage.toFixed(1) + '%' : 'N/A'}
        </div>
        <div class="text-xs text-slate-400">Memory Usage</div>
      </div>
      
      <div class="text-center">
        <div class="text-xl font-bold text-slate-100">
          {formatBytes(node.networkIn || 0)}/s
        </div>
        <div class="text-xs text-slate-400">Network In</div>
      </div>
      
      <div class="text-center">
        <div class="text-xl font-bold text-slate-100">
          {formatBytes(node.networkOut || 0)}/s
        </div>
        <div class="text-xs text-slate-400">Network Out</div>
      </div>
    </div>

    <!-- Health Bars -->
    {#if node.cpuUsage !== undefined || node.memoryUsage !== undefined || node.diskUsage !== undefined}
      <div class="space-y-3 mb-4">
        {#if node.cpuUsage !== undefined}
          <div>
            <div class="flex justify-between text-sm mb-1">
              <span class="text-slate-400">CPU Usage</span>
              <span class="text-slate-300">{node.cpuUsage.toFixed(1)}%</span>
            </div>
            <div class="w-full bg-slate-700 rounded-full h-2">
              <div 
                class="h-2 rounded-full transition-all duration-300 {
                  node.cpuUsage > 80 ? 'bg-red-500' :
                  node.cpuUsage > 60 ? 'bg-yellow-500' :
                  'bg-green-500'
                }" 
                style="width: {Math.min(node.cpuUsage, 100)}%"
              ></div>
            </div>
          </div>
        {/if}
        
        {#if node.memoryUsage !== undefined}
          <div>
            <div class="flex justify-between text-sm mb-1">
              <span class="text-slate-400">Memory Usage</span>
              <span class="text-slate-300">{node.memoryUsage.toFixed(1)}%</span>
            </div>
            <div class="w-full bg-slate-700 rounded-full h-2">
              <div 
                class="h-2 rounded-full transition-all duration-300 {
                  node.memoryUsage > 80 ? 'bg-red-500' :
                  node.memoryUsage > 60 ? 'bg-yellow-500' :
                  'bg-green-500'
                }" 
                style="width: {Math.min(node.memoryUsage, 100)}%"
              ></div>
            </div>
          </div>
        {/if}
        
        {#if node.diskUsage !== undefined}
          <div>
            <div class="flex justify-between text-sm mb-1">
              <span class="text-slate-400">Disk Usage</span>
              <span class="text-slate-300">{node.diskUsage.toFixed(1)}%</span>
            </div>
            <div class="w-full bg-slate-700 rounded-full h-2">
              <div 
                class="h-2 rounded-full transition-all duration-300 {
                  node.diskUsage > 80 ? 'bg-red-500' :
                  node.diskUsage > 60 ? 'bg-yellow-500' :
                  'bg-green-500'
                }" 
                style="width: {Math.min(node.diskUsage, 100)}%"
              ></div>
            </div>
          </div>
        {/if}
      </div>
    {/if}

    <!-- Additional Details -->
    <div class="border-t border-slate-700 pt-4">
      <div class="grid grid-cols-1 md:grid-cols-3 gap-4 text-sm">
        {#if node.uptime}
          <div>
            <span class="text-slate-400">Uptime:</span>
            <span class="text-slate-100 ml-2">{formatUptime(node.uptime)}</span>
          </div>
        {/if}
        
        {#if node.version}
          <div>
            <span class="text-slate-400">Version:</span>
            <span class="text-slate-100 ml-2">{node.version}</span>
          </div>
        {/if}
        
        {#if node.streamCount !== undefined}
          <div>
            <span class="text-slate-400">Active Streams:</span>
            <span class="text-slate-100 ml-2">{node.streamCount}</span>
          </div>
        {/if}
        
        {#if node.latitude && node.longitude}
          <div>
            <span class="text-slate-400">Coordinates:</span>
            <span class="text-slate-100 ml-2">{node.latitude.toFixed(4)}, {node.longitude.toFixed(4)}</span>
          </div>
        {/if}
        
        {#if node.nodeUrl}
          <div class="md:col-span-2">
            <span class="text-slate-400">URL:</span>
            <span class="text-slate-100 ml-2 font-mono text-xs">{node.nodeUrl}</span>
          </div>
        {/if}
      </div>
      
      {#if node.lastUpdated}
        <div class="mt-3 text-xs text-slate-500">
          Last updated: {formatDate(node.lastUpdated)}
        </div>
      {/if}
    </div>
  {:else}
    <!-- Compact view -->
    <div class="flex items-center justify-between text-sm">
      <div class="flex space-x-4">
        {#if node.cpuUsage !== undefined}
          <span class="text-slate-400">
            CPU: <span class="text-slate-100">{node.cpuUsage.toFixed(1)}%</span>
          </span>
        {/if}
        {#if node.memoryUsage !== undefined}
          <span class="text-slate-400">
            RAM: <span class="text-slate-100">{node.memoryUsage.toFixed(1)}%</span>
          </span>
        {/if}
        {#if node.streamCount !== undefined}
          <span class="text-slate-400">
            Streams: <span class="text-slate-100">{node.streamCount}</span>
          </span>
        {/if}
      </div>
      
      {#if node.lastUpdated}
        <div class="text-slate-500">
          {formatDate(node.lastUpdated)}
        </div>
      {/if}
    </div>
  {/if}

  <!-- Actions -->
  <div class="flex items-center justify-end mt-4 space-x-2">
    <slot name="actions" {node} />
    
    {#if node.nodeId}
      <button 
        class="text-blue-400 hover:text-blue-300 text-sm font-medium transition-colors"
        on:click={() => {/* Navigate to node details */}}
      >
        View Details
      </button>
    {/if}
  </div>
</div>