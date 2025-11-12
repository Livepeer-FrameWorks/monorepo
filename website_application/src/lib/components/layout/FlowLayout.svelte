<script lang="ts">
  import type { Snippet } from "svelte";

  interface Props {
    /** Content to render inside the flow layout */
    children: Snippet;
    /** Minimum width for each item before wrapping */
    minWidth?: "sm" | "md" | "lg";
    /** Gap between items */
    gap?: "tight" | "default" | "loose";
    /** Additional CSS classes */
    class?: string;
  }

  let {
    children,
    minWidth = "md",
    gap = "default",
    class: className = "",
  }: Props = $props();

  const flowClass = $derived(() => {
    const classes = ["flow-layout"];
    if (minWidth === "sm") classes.push("flow-layout--sm");
    if (minWidth === "lg") classes.push("flow-layout--lg");
    if (gap === "tight") classes.push("flow-layout--tight");
    if (gap === "loose") classes.push("flow-layout--loose");
    if (className) classes.push(className);
    return classes.join(" ");
  });
</script>

<div class={flowClass()}>
  {@render children()}
</div>
