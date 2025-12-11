<script lang="ts">
  import type { Snippet } from "svelte";

  interface Props {
    /** Content to render inside the panel */
    children: Snippet;
    /** Visual variant */
    variant?: "default" | "glass";
    /** Padding variant */
    padding?: "compact" | "default" | "spacious";
    /** Additional CSS classes */
    class?: string;
  }

  let {
    children,
    variant = "default",
    padding = "default",
    class: className = "",
  }: Props = $props();

  const panelClass = $derived.by(() => {
    const classes = ["panel"];
    if (variant === "glass") classes.push("panel--glass");
    if (padding === "compact") classes.push("panel--compact");
    if (padding === "spacious") classes.push("panel--spacious");
    if (className) classes.push(className);
    return classes.join(" ");
  });
</script>

<div class={panelClass}>
  {@render children()}
</div>
