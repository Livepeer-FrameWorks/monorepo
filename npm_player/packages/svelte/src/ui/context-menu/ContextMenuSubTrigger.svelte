<script lang="ts">
  import { ContextMenu } from "bits-ui";
  import { cn } from "@livepeer-frameworks/player-core";

  let {
    children,
    class: className,
    inset = false,
    ...rest
  }: { children?: any; class?: string; inset?: boolean } = $props();
</script>

<ContextMenu.SubTrigger
  class={cn(
    "flex cursor-default select-none items-center rounded-sm px-2 py-1.5 text-sm outline-none focus:bg-[#292e42] focus:text-[#c0caf5] data-[state=open]:bg-[#292e42] data-[state=open]:text-[#c0caf5] data-[highlighted]:bg-[#292e42] data-[highlighted]:text-[#c0caf5]",
    inset && "pl-8",
    className
  )}
  {...rest}
>
  {@render children?.()}
  <svg
    xmlns="http://www.w3.org/2000/svg"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="2"
    stroke-linecap="round"
    stroke-linejoin="round"
    class="ml-auto h-4 w-4"
  >
    <polyline points="9 18 15 12 9 6" />
  </svg>
</ContextMenu.SubTrigger>
