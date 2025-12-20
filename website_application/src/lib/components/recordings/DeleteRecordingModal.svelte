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

  interface DeleteRecordingData {
    dvrHash: string;
    internalName?: string | null;
  }

  interface Props {
    open: boolean;
    recording: DeleteRecordingData | null;
    deleting: boolean;
    onConfirm: () => void;
    onCancel: () => void;
  }

  let { open, recording, deleting, onConfirm, onCancel }: Props = $props();
</script>

<Dialog
  {open}
  onOpenChange={(value) => {
    if (!value) onCancel();
  }}
>
  <DialogContent class="max-w-md rounded-none border-[hsl(var(--tn-fg-gutter)/0.3)] bg-background p-0 gap-0 overflow-hidden">
    <DialogHeader class="slab-header text-left space-y-1">
      <DialogTitle class="uppercase tracking-wide text-sm font-semibold text-muted-foreground">Delete Recording</DialogTitle>
      <DialogDescription class="text-xs text-muted-foreground/70">
        This action cannot be undone. The recording files will be permanently removed.
      </DialogDescription>
    </DialogHeader>

    <div class="slab-body--padded">
      <p class="text-sm text-muted-foreground">
        Are you sure you want to delete the recording for
        <span class="font-semibold text-foreground"
          >"{recording?.internalName || recording?.dvrHash}"</span
        >?
      </p>
    </div>

    <DialogFooter class="slab-actions slab-actions--row gap-0">
      <Button
        variant="ghost"
        class="rounded-none h-12 flex-1 border-r border-[hsl(var(--tn-fg-gutter)/0.3)] hover:bg-muted/10 text-muted-foreground hover:text-foreground"
        onclick={onCancel}
        disabled={deleting}
      >
        Cancel
      </Button>
      <Button
        variant="ghost"
        class="rounded-none h-12 flex-1 hover:bg-destructive/10 text-destructive hover:text-destructive"
        onclick={onConfirm}
        disabled={deleting}
      >
        {deleting ? "Deleting..." : "Delete Recording"}
      </Button>
    </DialogFooter>
  </DialogContent>
</Dialog>
