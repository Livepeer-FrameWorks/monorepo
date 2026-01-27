<script lang="ts">
  interface Props {
    size?: number;
    class?: string;
  }

  let { size = 14, class: className = "" }: Props = $props();
</script>

<svg
  width={size}
  height={size}
  viewBox="0 0 24 24"
  fill="none"
  stroke="currentColor"
  stroke-width="2"
  stroke-linecap="round"
  stroke-linejoin="round"
  class={className}
>
  <polyline points="11 17 6 12 11 7"></polyline>
  <polyline points="18 17 13 12 18 7"></polyline>
</svg>
