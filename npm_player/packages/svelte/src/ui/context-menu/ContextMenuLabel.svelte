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

<ContextMenu.GroupHeading
  class={cn(
    "fw-context-menu-label",
    inset && "fw-context-menu-item--inset",
    className
  )}
  {...rest}
>
  {@render children?.()}
</ContextMenu.GroupHeading>
