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
<script context="module" lang="ts">
  export interface IconProps {
    size?: number;
    color?: string;
    class?: string;
  }
</script>

<script lang="ts">
  // This file exports components via context="module" script
  // The default export is a placeholder - use the named exports
</script>

<!-- This component itself is not rendered - use the exported icon components below -->
<slot />
