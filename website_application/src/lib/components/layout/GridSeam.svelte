<script lang="ts">
  import type { Snippet } from "svelte";

  interface Props {
    /** Content to render inside the grid */
    children: Snippet;
    /** Number of columns */
    cols?: 1 | 2 | 3 | 4;
    /** Visual surface variant */
    surface?: "panel" | "glass" | "transparent";
    /** Responsive stacking breakpoint (2x2 = 1col→2col→full for 4-item grids) */
    stack?: "sm" | "md" | "lg" | "2x2" | "none";
    /** Flush mode - zero gap, borders touch edges */
    flush?: boolean;
    /** Additional CSS classes */
    class?: string;
  }

  let {
    children,
    cols = 2,
    surface = "panel",
    stack = "md",
    flush = false,
    class: className = "",
  }: Props = $props();

  const gridClass = $derived.by(() => {
    const classes = ["grid-seam", `grid-seam--cols-${cols}`];
    if (stack !== "none") classes.push(`grid-seam--stack-${stack}`);
    if (flush) classes.push("grid-seam--flush");
    if (className) classes.push(className);
    return classes.join(" ");
  });
</script>

<div class={gridClass} data-surface={surface}>
  {@render children()}
</div>
