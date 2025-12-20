<script lang="ts">
  import { ContextMenu } from "bits-ui";

  let { children, ...rest }: { children?: any } = $props();
</script>

<ContextMenu.Portal {...rest}>
  <div class="fw-player-surface">
    {@render children?.()}
  </div>
</ContextMenu.Portal>
