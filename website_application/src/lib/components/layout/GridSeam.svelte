<script lang="ts">
  import type { Snippet } from "svelte";

  interface Props {
    /** Content to render inside the grid */
    children: Snippet;
    /** Number of columns */
    cols?: 1 | 2 | 3 | 4;
    /** Visual surface variant */
    surface?: "panel" | "glass";
    /** Responsive stacking breakpoint */
    stack?: "sm" | "md" | "lg" | "none";
    /** Additional CSS classes */
    class?: string;
  }

  let {
    children,
    cols = 2,
    surface = "panel",
    stack = "md",
    class: className = "",
  }: Props = $props();

  const gridClass = $derived(() => {
    const classes = ["grid-seam", `grid-seam--cols-${cols}`];
    if (stack !== "none") classes.push(`grid-seam--stack-${stack}`);
    if (className) classes.push(className);
    return classes.join(" ");
  });
</script>

<div class={gridClass()} data-surface={surface}>
  {@render children()}
</div>
