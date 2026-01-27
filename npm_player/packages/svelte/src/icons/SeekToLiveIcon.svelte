<!--
  SeekToLiveIcon.svelte - Play triangle + end bar for seeking to live
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
  <!-- Play triangle -->
  <path d="M5 5v14l11-7z" fill={color} />
  <!-- End bar -->
  <rect x="17" y="5" width="3" height="14" fill={color} />
</svg>
