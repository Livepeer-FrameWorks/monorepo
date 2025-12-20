<script lang="ts">
  import { ContextMenu } from "bits-ui";
  import { cn } from "@livepeer-frameworks/player-core";

  let {
    children,
    class: className,
    ...rest
  }: { children?: any; class?: string } = $props();
</script>

<ContextMenu.SubContent
  class={cn(
    "z-50 min-w-[8rem] overflow-hidden rounded border border-[#5a607f]/30 bg-[#1a1b26] p-0 text-[#a9b1d6] shadow-lg data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0 data-[state=closed]:zoom-out-95 data-[state=open]:zoom-in-95 data-[side=bottom]:slide-in-from-top-2 data-[side=left]:slide-in-from-right-2 data-[side=right]:slide-in-from-left-2 data-[side=top]:slide-in-from-bottom-2",
    className
  )}
  {...rest}
>
  {@render children?.()}
</ContextMenu.SubContent>
