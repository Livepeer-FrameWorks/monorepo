<script lang="ts">
  import { ContextMenu } from "bits-ui";
  import { cn } from "@livepeer-frameworks/player-core";

  let {
    children,
    class: className,
    inset = false,
    ...rest
  }: { children?: any; class?: string; inset?: boolean } & Record<string, any> = $props();
</script>

<ContextMenu.Item
  class={cn(
    "fw-context-menu-item",
    inset && "fw-context-menu-item--inset",
    className
  )}
  {...rest}
>
  {@render children?.()}
</ContextMenu.Item>
