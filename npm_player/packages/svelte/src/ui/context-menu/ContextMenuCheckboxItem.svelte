<script lang="ts">
  import type { Snippet } from "svelte";
  import { ContextMenu } from "bits-ui";
  import { cn } from "@livepeer-frameworks/player-core";

  let {
    class: className,
    checked = false,
    children,
    ...rest
  }: { class?: string; checked?: boolean; children?: Snippet; [key: string]: unknown } = $props();
</script>

<ContextMenu.CheckboxItem class={cn("fw-context-menu-checkbox", className)} {checked} {...rest}>
  <div class="fw-context-menu-indicator">
    {#if checked}
      <svg
        xmlns="http://www.w3.org/2000/svg"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        stroke-width="2"
        stroke-linecap="round"
        stroke-linejoin="round"
        class="h-4 w-4"
      >
        <polyline points="20 6 9 17 4 12" />
      </svg>
    {/if}
  </div>
  {@render children?.()}
</ContextMenu.CheckboxItem>
