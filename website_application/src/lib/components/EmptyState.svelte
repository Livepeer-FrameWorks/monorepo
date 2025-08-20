<script>
  import { getIconComponent } from '$lib/iconUtils.js';

  /** @type {string} */
  export let iconName = 'FileText';
  /** @type {string} */
  export let title = 'No data found';
  /** @type {string} */
  export let description = '';
  /** @type {string} */
  export let actionText = '';
  /** @type {() => void} */
  export let onAction = () => {};
  /** @type {'sm' | 'md' | 'lg'} */
  export let size = 'md';
  /** @type {string} */
  export let className = '';
  /** @type {boolean} */
  export let showAction = true;
  
  $: iconComponent = getIconComponent(iconName);

  const sizeClasses = {
    sm: {
      container: 'py-8',
      icon: 'w-8 h-8 mb-2 mx-auto text-tokyo-night-fg-dark',
      title: 'text-lg font-semibold mb-1',
      description: 'text-sm mb-4',
      button: 'px-4 py-2 text-sm'
    },
    md: {
      container: 'py-12',
      icon: 'w-12 h-12 mb-4 mx-auto text-tokyo-night-fg-dark',
      title: 'text-xl font-semibold mb-2',
      description: 'text-sm mb-6',
      button: 'px-6 py-3'
    },
    lg: {
      container: 'py-16',
      icon: 'w-16 h-16 mb-6 mx-auto text-tokyo-night-fg-dark',
      title: 'text-2xl font-bold mb-3',
      description: 'mb-8',
      button: 'px-8 py-4 text-lg'
    }
  };

  $: classes = sizeClasses[size];
</script>

<div class="text-center {classes.container} {className}">
  <!-- Icon -->
  <svelte:component this={iconComponent} class="{classes.icon}" />
  
  <!-- Title -->
  <h3 class="text-tokyo-night-fg {classes.title}">
    {title}
  </h3>
  
  <!-- Description -->
  {#if description}
    <p class="text-tokyo-night-fg-dark {classes.description}">
      {description}
    </p>
  {/if}
  
  <!-- Action Button -->
  {#if actionText && showAction}
    <button
      class="btn-primary {classes.button}"
      on:click={onAction}
    >
      {actionText}
    </button>
  {/if}
  
  <!-- Custom slot for additional content -->
  <slot />
</div>