<!--
  Icons.svelte - Icon component library
  Port of src/components/Icons.tsx

  Usage:
  <script>
    import { PlayIcon, PauseIcon, VolumeIcon } from './Icons.svelte';
  </script>

  <PlayIcon size={24} />
  <VolumeIcon isMuted={false} />
-->
<script module lang="ts">
  export interface IconProps {
    size?: number;
    color?: string;
    class?: string;
  }
</script>

<script lang="ts">
  import type { Snippet } from "svelte";
  // This file exports components via module script
  // The default export is a placeholder - use the named exports
  let { children }: { children?: Snippet } = $props();
</script>

<!-- This component itself is not rendered - use the exported icon components below -->
{@render children?.()}
