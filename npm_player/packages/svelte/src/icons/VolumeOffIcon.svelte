<!--
  VolumeOffIcon.svelte
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
  <polygon points="11,5 6,9 2,9 2,15 6,15 11,19" fill={color} />
  <line x1="23" y1="9" x2="17" y2="15" stroke={color} stroke-width="2" stroke-linecap="round" />
  <line x1="17" y1="9" x2="23" y2="15" stroke={color} stroke-width="2" stroke-linecap="round" />
</svg>
