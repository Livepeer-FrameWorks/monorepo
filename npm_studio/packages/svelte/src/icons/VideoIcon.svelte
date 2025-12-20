<script lang="ts">
  let { size = 14, active = false }: { size?: number; active?: boolean } = $props();
</script>

<svg width={size} height={size} viewBox="0 0 24 24" fill={active ? "currentColor" : "none"} stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
  <polygon points="23 7 16 12 23 17 23 7" />
  <rect x="1" y="5" width="15" height="14" rx="2" ry="2" />
</svg>
