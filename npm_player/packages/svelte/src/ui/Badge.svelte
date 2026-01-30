<script lang="ts">
  import type { Snippet } from "svelte";
  import { cn } from "@livepeer-frameworks/player-core";
  import { badgeVariants, type BadgeVariant } from "./badge";

  type Props = {
    variant?: BadgeVariant;
    class?: string;
    children?: Snippet;
    [key: string]: unknown;
  };

  let { variant = "default", class: className = "", children, ...rest }: Props = $props();

  let mergedClasses = $derived(cn(badgeVariants(variant, className)));
</script>

<div class={mergedClasses} {...rest}>
  {@render children?.()}
</div>
