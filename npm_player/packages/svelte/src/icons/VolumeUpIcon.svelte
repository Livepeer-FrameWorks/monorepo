<!--
  VolumeUpIcon.svelte
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
  <path
    d="M19.07 4.93a10 10 0 0 1 0 14.14M15.54 8.46a5 5 0 0 1 0 7.07"
    stroke={color}
    stroke-width="2"
    stroke-linecap="round"
    stroke-linejoin="round"
  />
</svg>
