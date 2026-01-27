<!--
  FullscreenExitIcon.svelte
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
  <path
    d="M8 3v3a2 2 0 0 1-2 2H3M21 8h-3a2 2 0 0 1-2-2V3M3 16h3a2 2 0 0 1 2 2v3M16 21v-3a2 2 0 0 1 2-2h3"
    stroke={color}
    stroke-width="2"
    stroke-linecap="round"
    stroke-linejoin="round"
  />
</svg>
