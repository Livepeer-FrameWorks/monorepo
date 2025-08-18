<script>
  /** @type {'sm' | 'md' | 'lg' | 'xl'} */
  export let size = 'md';
  /** @type {string} */
  export let color = 'text-tokyo-night-blue';
  /** @type {string} */
  export let className = '';

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