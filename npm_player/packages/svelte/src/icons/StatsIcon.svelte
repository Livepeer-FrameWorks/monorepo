<!--
  StatsIcon.svelte - Bar chart style for "stats for nerds"
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
  <rect x="4" y="13" width="4" height="7" fill={color} />
  <rect x="10" y="9" width="4" height="11" fill={color} />
  <rect x="16" y="4" width="4" height="16" fill={color} />
</svg>
