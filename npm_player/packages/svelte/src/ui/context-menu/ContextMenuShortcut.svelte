<script lang="ts">
  import { cn } from "@livepeer-frameworks/player-core";

  let {
    children,
    class: className,
    ...rest
  }: { children?: any; class?: string } = $props();
</script>

<span
  class={cn(
    "ml-auto text-xs tracking-widest text-muted-foreground",
    className
  )}
  {...rest}
>
  {@render children?.()}
</span>
