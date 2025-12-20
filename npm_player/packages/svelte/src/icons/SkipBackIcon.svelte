<!--
  SkipBackIcon.svelte - Circular rewind arrow with "10"
-->
<script lang="ts">
  interface Props {
    size?: number;
    color?: string;
    class?: string;
  }

  let {
    size = 16,
    color = 'currentColor',
    class: className = '',
  }: Props = $props();
</script>

<svg
  width={size}
  height={size}
  viewBox="0 0 24 24"
  fill="none"
  class={className}
  aria-hidden="true"
>
  <!-- Circular rewind arrow -->
  <path
    d="M12 5V1L7 6l5 5V7c3.31 0 6 2.69 6 6s-2.69 6-6 6-6-2.69-6-6H4c0 4.42 3.58 8 8 8s8-3.58 8-8-3.58-8-8-8z"
    fill={color}
  />
  <text x="12" y="15" font-size="7" font-weight="bold" fill={color} text-anchor="middle">10</text>
</svg>
