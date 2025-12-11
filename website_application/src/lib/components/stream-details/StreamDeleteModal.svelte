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

  let {
    open = $bindable(false),
    streamName = "",
    loading = false,
    onConfirm,
  } = $props();
</script>

<Dialog {open} onOpenChange={(value) => (open = value)}>
  <DialogContent class="max-w-md backdrop-blur-sm">
    <DialogHeader>
      <DialogTitle class="text-destructive">Delete Stream</DialogTitle>
      <DialogDescription>
        This action cannot be undone. All associated keys and recordings will
        also be removed.
      </DialogDescription>
    </DialogHeader>

    <div class="p-4 bg-destructive/10 border border-destructive/30">
      <p class="text-sm text-foreground">
        Are you sure you want to delete
        <strong class="text-destructive">{streamName}</strong>?
      </p>
    </div>

    <DialogFooter class="gap-2">
      <Button variant="outline" onclick={() => (open = false)}>
        Cancel
      </Button>
      <Button
        variant="destructive"
        class="gap-2 transition-all hover:shadow-lg hover:shadow-destructive/50"
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
