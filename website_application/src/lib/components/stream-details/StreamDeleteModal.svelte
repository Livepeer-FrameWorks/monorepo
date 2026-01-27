<script lang="ts">
  import { Button } from "$lib/components/ui/button";
  import {
    Dialog,
    DialogContent,
    DialogDescription,
    DialogFooter,
    DialogHeader,
    DialogTitle,
  } from "$lib/components/ui/dialog";
  import { getIconComponent } from "$lib/iconUtils";

  let { open = $bindable(false), streamName = "", loading = false, onConfirm } = $props();
</script>

<Dialog {open} onOpenChange={(value) => (open = value)}>
  <DialogContent
    class="max-w-md rounded-none border-[hsl(var(--tn-fg-gutter)/0.3)] bg-background p-0 gap-0 overflow-hidden"
  >
    <DialogHeader class="slab-header text-left space-y-1">
      <DialogTitle class="uppercase tracking-wide text-sm font-semibold text-muted-foreground"
        >Delete Stream</DialogTitle
      >
      <DialogDescription class="text-xs text-muted-foreground/70">
        This action cannot be undone. All associated keys and recordings will also be removed.
      </DialogDescription>
    </DialogHeader>

    <div class="slab-body--padded">
      <p class="text-sm text-muted-foreground">
        Are you sure you want to delete
        <strong class="text-foreground">{streamName}</strong>?
      </p>
    </div>

    <DialogFooter class="slab-actions slab-actions--row gap-0">
      <Button
        variant="ghost"
        class="rounded-none h-12 flex-1 border-r border-[hsl(var(--tn-fg-gutter)/0.3)] hover:bg-muted/10 text-muted-foreground hover:text-foreground"
        onclick={() => (open = false)}
      >
        Cancel
      </Button>
      <Button
        variant="ghost"
        class="rounded-none h-12 flex-1 hover:bg-destructive/10 text-destructive hover:text-destructive gap-2"
        onclick={onConfirm}
        disabled={loading}
      >
        {#if loading}
          {@const SvelteComponent = getIconComponent("Loader")}
          <SvelteComponent class="w-4 h-4 animate-spin" />
        {/if}
        Delete Stream
      </Button>
    </DialogFooter>
  </DialogContent>
</Dialog>
