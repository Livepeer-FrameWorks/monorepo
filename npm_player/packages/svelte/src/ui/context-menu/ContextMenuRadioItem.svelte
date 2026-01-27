<script lang="ts">
  import { ContextMenu } from "bits-ui";
  import { cn } from "@livepeer-frameworks/player-core";

  let {
    class: className,
    value,
    ...rest
  }: { class?: string; value: string } & Record<string, any> = $props();
</script>

<ContextMenu.RadioItem class={cn("fw-context-menu-checkbox", className)} {value} {...rest}>
  <div class="fw-context-menu-indicator">
    <!-- Indicator state is managed by the underlying ContextMenu.RadioGroup. -->
  </div>
  <slot />
</ContextMenu.RadioItem>
