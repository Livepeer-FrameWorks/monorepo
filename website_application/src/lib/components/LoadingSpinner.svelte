<script>
  
  
  
  /**
   * @typedef {Object} Props
   * @property {'sm' | 'md' | 'lg' | 'xl'} [size]
   * @property {string} [color]
   * @property {string} [className]
   */

  /** @type {Props} */
  let { size = 'md', color = 'text-tokyo-night-blue', className = '' } = $props();

  const sizeClasses = {
    sm: 'w-4 h-4',
    md: 'w-6 h-6', 
    lg: 'w-8 h-8',
    xl: 'w-12 h-12'
  };
</script>

<div 
  class="inline-block border-2 border-current border-t-transparent rounded-full animate-spin {sizeClasses[size]} {color} {className}"
  role="status"
  aria-label="Loading"
>
  <span class="sr-only">Loading...</span>
</div>