<script lang="ts">
  import type { Snippet } from "svelte";

  interface Props {
    /** Content to render inside the band */
    children: Snippet;
    /** Visual surface variant */
    surface?: "default" | "subtle" | "elevated";
    /** Vertical spacing variant */
    spacing?: "compact" | "default" | "spacious";
    /** Apply container max-width to content */
    container?: boolean;
    /** Container width variant (only when container=true) */
    containerWidth?: "narrow" | "default" | "wide" | "full";
    /** Flush mode - zero inner padding, content touches edges */
    flush?: boolean;
    /** Additional CSS classes */
    class?: string;
  }

  let {
    children,
    surface = "default",
    spacing = "default",
    container = false,
    containerWidth = "default",
    flush = false,
    class: className = "",
  }: Props = $props();

  const bandClass = $derived.by(() => {
    const classes = ["band"];
    if (spacing === "compact") classes.push("band--compact");
    if (spacing === "spacious") classes.push("band--spacious");
    if (flush) classes.push("band--flush");
    if (className) classes.push(className);
    return classes.join(" ");
  });

  const containerClass = $derived.by(() => {
    if (!container) return "";
    const classes = ["app-container"];
    if (containerWidth === "narrow") classes.push("app-container--narrow");
    if (containerWidth === "wide") classes.push("app-container--wide");
    if (containerWidth === "full") classes.push("app-container--full");
    return classes.join(" ");
  });

  const surfaceAttr = $derived(surface !== "default" ? surface : undefined);
</script>

<section class={bandClass} data-surface={surfaceAttr}>
  {#if container}
    <div class={containerClass}>
      {@render children()}
    </div>
  {:else}
    {@render children()}
  {/if}
</section>
