<script lang="ts">
  import { Switch as SwitchPrimitive } from "bits-ui";
  import { cn, type WithoutChildrenOrChild } from "$lib/utils";

  let {
    ref = $bindable(null),
    class: className,
    checked = $bindable(false),
    ...restProps
  }: WithoutChildrenOrChild<SwitchPrimitive.RootProps> = $props();
</script>

<SwitchPrimitive.Root
  bind:ref
  bind:checked
  data-slot="switch"
  class={cn(
    "data-[state=checked]:bg-primary data-[state=unchecked]:bg-input focus-visible:border-primary/90 focus-visible:ring-primary/35 dark:data-[state=unchecked]:bg-input/80 shadow-[0_2px_8px_rgba(6,15,65,0.12)] peer inline-flex h-[1.15rem] w-8 shrink-0 items-center rounded-full border border-transparent outline-none transition-all focus-visible:ring-[2px] disabled:cursor-not-allowed disabled:opacity-50",
    className
  )}
  {...restProps}
>
  <SwitchPrimitive.Thumb
    data-slot="switch-thumb"
    class={cn(
      "bg-background dark:data-[state=unchecked]:bg-foreground dark:data-[state=checked]:bg-primary-foreground pointer-events-none block size-4 rounded-full ring-0 transition-transform data-[state=checked]:translate-x-[calc(100%-2px)] data-[state=unchecked]:translate-x-0"
    )}
  />
</SwitchPrimitive.Root>
