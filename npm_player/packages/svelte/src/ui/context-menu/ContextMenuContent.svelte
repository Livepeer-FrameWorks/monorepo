<script lang="ts">
  import { ContextMenu } from "bits-ui";
  import { cn } from "@livepeer-frameworks/player-core";

  let { children, class: className, ...rest }: { children?: any; class?: string } = $props();
</script>

<ContextMenu.Content class={cn("fw-player-surface fw-context-menu", className)} {...rest}>
  {@render children?.()}
</ContextMenu.Content>
