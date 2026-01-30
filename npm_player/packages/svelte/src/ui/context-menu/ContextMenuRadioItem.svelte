<script lang="ts">
  import type { Snippet } from "svelte";
  import { ContextMenu } from "bits-ui";
  import { cn } from "@livepeer-frameworks/player-core";

  let {
    class: className,
    value,
    children,
    ...rest
  }: { class?: string; value: string; children?: Snippet; [key: string]: unknown } = $props();
</script>

<ContextMenu.RadioItem class={cn("fw-context-menu-checkbox", className)} {value} {...rest}>
  <div class="fw-context-menu-indicator">
    <!-- Indicator state is managed by the underlying ContextMenu.RadioGroup. -->
  </div>
  {@render children?.()}
</ContextMenu.RadioItem>
