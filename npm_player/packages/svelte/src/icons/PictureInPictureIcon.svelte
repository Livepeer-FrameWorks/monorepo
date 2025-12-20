<!--
  PictureInPictureIcon.svelte
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
  <rect x="2" y="3" width="20" height="14" rx="2" ry="2" stroke={color} stroke-width="2" fill="none" />
  <rect x="8" y="10" width="10" height="6" rx="1" ry="1" fill={color} />
</svg>
