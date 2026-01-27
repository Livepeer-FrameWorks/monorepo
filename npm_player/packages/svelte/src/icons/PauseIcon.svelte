<!--
  PauseIcon.svelte
-->
<script lang="ts">
  interface Props {
    size?: number;
    color?: string;
    class?: string;
  }

  let { size = 16, color = "currentColor", class: className = "" }: Props = $props();
</script>

<svg
  width={size}
  height={size}
  viewBox="0 0 24 24"
  fill="none"
  class={className}
  aria-hidden="true"
>
  <rect x="6" y="4" width="4" height="16" fill={color} />
  <rect x="14" y="4" width="4" height="16" fill={color} />
</svg>
