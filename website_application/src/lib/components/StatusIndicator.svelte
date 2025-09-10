<script>
  import { getIconComponent } from '$lib/iconUtils.js';
  
  export let status = '';
  export let size = 'normal'; // 'small', 'normal', 'large'
  export let showLabel = true;
  export let pulse = false;
  
  $: statusConfig = getStatusConfig(status);
  $: IconComponent = statusConfig.icon;
  
  /**
   * @param {string | null | undefined} status
   */
  function getStatusConfig(status) {
    const normalizedStatus = status?.toLowerCase() || 'unknown';
    
    /** @type {Record<string, {color: string, label: string, icon: any}>} */
    const configs = {
      // Stream statuses
      'live': { color: 'green', label: 'Live', icon: getIconComponent('Circle') },
      'online': { color: 'green', label: 'Online', icon: getIconComponent('Circle') },
      'active': { color: 'green', label: 'Active', icon: getIconComponent('Circle') },
      'healthy': { color: 'green', label: 'Healthy', icon: getIconComponent('CheckCircle') },
      'connected': { color: 'green', label: 'Connected', icon: getIconComponent('Wifi') },
      'recording': { color: 'blue', label: 'Recording', icon: getIconComponent('Circle') },
      'processing': { color: 'blue', label: 'Processing', icon: getIconComponent('RefreshCw') },
      'pending': { color: 'blue', label: 'Pending', icon: getIconComponent('Clock') },
      'buffering': { color: 'yellow', label: 'Buffering', icon: getIconComponent('Clock') },
      'paused': { color: 'yellow', label: 'Paused', icon: getIconComponent('PauseCircle') },
      'warning': { color: 'yellow', label: 'Warning', icon: getIconComponent('AlertTriangle') },
      'degraded': { color: 'yellow', label: 'Degraded', icon: getIconComponent('AlertTriangle') },
      'offline': { color: 'red', label: 'Offline', icon: getIconComponent('CircleX') },
      'error': { color: 'red', label: 'Error', icon: getIconComponent('XCircle') },
      'failed': { color: 'red', label: 'Failed', icon: getIconComponent('XCircle') },
      'disconnected': { color: 'red', label: 'Disconnected', icon: getIconComponent('WifiOff') },
      'unhealthy': { color: 'red', label: 'Unhealthy', icon: getIconComponent('XCircle') },
      'stopped': { color: 'gray', label: 'Stopped', icon: getIconComponent('StopCircle') },
      'inactive': { color: 'gray', label: 'Inactive', icon: getIconComponent('Circle') },
      'disabled': { color: 'gray', label: 'Disabled', icon: getIconComponent('CircleSlash') },
      'maintenance': { color: 'gray', label: 'Maintenance', icon: getIconComponent('Wrench') },
      'unknown': { color: 'gray', label: 'Unknown', icon: getIconComponent('HelpCircle') }
    };
    
    return configs[normalizedStatus] || configs.unknown;
  }
  
  /**
   * @param {string} color
   */
  function getColorClasses(color) {
    /** @type {Record<string, {bg: string, text: string, border: string}>} */
    const colors = {
      green: {
        bg: 'bg-tokyo-night-green/20',
        text: 'text-tokyo-night-green',
        border: 'border-tokyo-night-green/30'
      },
      blue: {
        bg: 'bg-tokyo-night-blue/20',
        text: 'text-tokyo-night-blue',
        border: 'border-tokyo-night-blue/30'
      },
      yellow: {
        bg: 'bg-tokyo-night-yellow/20',
        text: 'text-tokyo-night-yellow',
        border: 'border-tokyo-night-yellow/30'
      },
      red: {
        bg: 'bg-tokyo-night-red/20',
        text: 'text-tokyo-night-red',
        border: 'border-tokyo-night-red/30'
      },
      gray: {
        bg: 'bg-tokyo-night-comment/20',
        text: 'text-tokyo-night-comment',
        border: 'border-tokyo-night-comment/30'
      }
    };
    
    return colors[color] || colors.gray;
  }
  
  $: colorClasses = getColorClasses(statusConfig.color);
  $: sizeClasses = {
    small: {
      container: 'px-2 py-0.5 text-xs',
      icon: 'w-3 h-3',
      spacing: 'mr-1'
    },
    normal: {
      container: 'px-2.5 py-0.5 text-xs',
      icon: 'w-3.5 h-3.5',
      spacing: 'mr-1.5'
    },
    large: {
      container: 'px-3 py-1 text-sm',
      icon: 'w-4 h-4',
      spacing: 'mr-2'
    }
  }[size] || {
    container: 'px-2.5 py-0.5 text-xs',
    icon: 'w-3.5 h-3.5',
    spacing: 'mr-1.5'
  };
</script>

<span class="inline-flex items-center {sizeClasses.container} rounded-full font-medium {colorClasses.bg} {colorClasses.text} border {colorClasses.border}">
  {#if IconComponent}
    <svelte:component 
      this={IconComponent} 
      class="{sizeClasses.icon} {sizeClasses.spacing} {pulse ? 'animate-pulse' : ''}" 
    />
  {/if}
  {#if showLabel}
    {statusConfig.label}
  {/if}
</span>