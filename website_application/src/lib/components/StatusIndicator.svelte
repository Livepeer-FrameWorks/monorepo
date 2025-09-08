<script>
  export let status = '';
  export let size = 'normal'; // 'small', 'normal', 'large'
  export let showLabel = true;
  export let pulse = false;
  
  $: statusConfig = getStatusConfig(status);
  
  /**
   * @param {string | null | undefined} status
   */
  function getStatusConfig(status) {
    const normalizedStatus = status?.toLowerCase() || 'unknown';
    
    /** @type {Record<string, {color: string, label: string, icon: string}>} */
    const configs = {
      // Stream statuses
      'live': { color: 'green', label: 'Live', icon: '●' },
      'online': { color: 'green', label: 'Online', icon: '●' },
      'active': { color: 'green', label: 'Active', icon: '●' },
      'healthy': { color: 'green', label: 'Healthy', icon: '✓' },
      'connected': { color: 'green', label: 'Connected', icon: '●' },
      'recording': { color: 'blue', label: 'Recording', icon: '●' },
      'processing': { color: 'blue', label: 'Processing', icon: '⟳' },
      'pending': { color: 'blue', label: 'Pending', icon: '⏳' },
      'buffering': { color: 'yellow', label: 'Buffering', icon: '⏳' },
      'paused': { color: 'yellow', label: 'Paused', icon: '⏸' },
      'warning': { color: 'yellow', label: 'Warning', icon: '⚠' },
      'degraded': { color: 'yellow', label: 'Degraded', icon: '⚠' },
      'offline': { color: 'red', label: 'Offline', icon: '●' },
      'error': { color: 'red', label: 'Error', icon: '✗' },
      'failed': { color: 'red', label: 'Failed', icon: '✗' },
      'disconnected': { color: 'red', label: 'Disconnected', icon: '●' },
      'unhealthy': { color: 'red', label: 'Unhealthy', icon: '✗' },
      'stopped': { color: 'gray', label: 'Stopped', icon: '●' },
      'inactive': { color: 'gray', label: 'Inactive', icon: '●' },
      'disabled': { color: 'gray', label: 'Disabled', icon: '●' },
      'maintenance': { color: 'gray', label: 'Maintenance', icon: '🔧' },
      'unknown': { color: 'gray', label: 'Unknown', icon: '?' }
    };
    
    return configs[normalizedStatus] || configs.unknown;
  }
  
  /**
   * @param {string} color
   */
  function getColorClasses(color) {
    /** @type {Record<string, {bg: string, text: string, border: string, dot: string}>} */
    const colors = {
      green: {
        bg: 'bg-green-900/30',
        text: 'text-green-400',
        border: 'border-green-500/30',
        dot: 'bg-green-400'
      },
      blue: {
        bg: 'bg-blue-900/30',
        text: 'text-blue-400',
        border: 'border-blue-500/30',
        dot: 'bg-blue-400'
      },
      yellow: {
        bg: 'bg-yellow-900/30',
        text: 'text-yellow-400',
        border: 'border-yellow-500/30',
        dot: 'bg-yellow-400'
      },
      red: {
        bg: 'bg-red-900/30',
        text: 'text-red-400',
        border: 'border-red-500/30',
        dot: 'bg-red-400'
      },
      gray: {
        bg: 'bg-gray-900/30',
        text: 'text-gray-400',
        border: 'border-gray-500/30',
        dot: 'bg-gray-400'
      }
    };
    
    return colors[color] || colors.gray;
  }
  
  $: colorClasses = getColorClasses(statusConfig.color);
  $: sizeClasses = {
    small: {
      container: 'px-2 py-0.5 text-xs',
      dot: 'w-1.5 h-1.5',
      spacing: 'mr-1'
    },
    normal: {
      container: 'px-2.5 py-0.5 text-xs',
      dot: 'w-2 h-2',
      spacing: 'mr-1.5'
    },
    large: {
      container: 'px-3 py-1 text-sm',
      dot: 'w-2.5 h-2.5',
      spacing: 'mr-2'
    }
  }[size] || {
    container: 'px-2.5 py-0.5 text-xs',
    dot: 'w-2 h-2',
    spacing: 'mr-1.5'
  };
</script>

<span class="inline-flex items-center {sizeClasses.container} rounded-full font-medium {colorClasses.bg} {colorClasses.text} border {colorClasses.border}">
  <span class="{sizeClasses.dot} rounded-full {sizeClasses.spacing} {colorClasses.dot} {pulse ? 'animate-pulse' : ''}"></span>
  {#if showLabel}
    {statusConfig.label}
  {/if}
</span>