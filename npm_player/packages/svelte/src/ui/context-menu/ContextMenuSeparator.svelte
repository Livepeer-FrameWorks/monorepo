<script lang="ts">
  import { ContextMenu } from "bits-ui";
  import { cn } from "@livepeer-frameworks/player-core";

  let { class: className, ...rest }: { class?: string } = $props();
</script>

<ContextMenu.Separator class={cn("fw-context-menu-separator", className)} {...rest} />
