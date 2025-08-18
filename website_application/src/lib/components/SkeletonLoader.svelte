<script>
  /** @type {'text' | 'text-sm' | 'text-lg' | 'card' | 'avatar' | 'chart' | 'metric' | 'table-row' | 'custom'} */
  export let type = 'text';
  /** @type {string} */
  export let className = '';
  /** @type {number} */
  export let count = 1;
  /** @type {boolean} */
  export let inline = false;

  const baseClasses = {
    'text': 'skeleton-text',
    'text-sm': 'skeleton-text-sm',
    'text-lg': 'skeleton-text-lg',
    'card': 'skeleton-card',
    'avatar': 'skeleton-avatar',
    'chart': 'skeleton-chart',
    'metric': 'skeleton-metric',
    'table-row': 'skeleton-table-row',
    'custom': 'skeleton'
  };

  const containerClass = inline ? 'inline-flex' : 'space-y-2';
</script>

<div class={containerClass}>
  {#each Array(count) as _, i}
    <div 
      class="{baseClasses[type]} {className}"
      style="animation-delay: {i * 0.1}s"
      role="presentation"
      aria-hidden="true"
    />
  {/each}
</div>