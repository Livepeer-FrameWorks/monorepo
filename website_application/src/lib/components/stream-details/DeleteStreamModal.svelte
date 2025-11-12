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

  interface Stream {
    id: string;
    name?: string;
  }

  interface Props {
    open: boolean;
    stream: Stream | null;
    deleting: boolean;
    onConfirm: () => void;
    onCancel: () => void;
  }

  let { open, stream, deleting, onConfirm, onCancel }: Props = $props();
</script>

<Dialog
  {open}
  onOpenChange={(value) => {
    if (!value) onCancel();
  }}
>
  <DialogContent class="max-w-md">
    <DialogHeader>
      <DialogTitle>Delete Stream</DialogTitle>
      <DialogDescription>
        This action cannot be undone and will remove associated keys and
        recordings.
      </DialogDescription>
    </DialogHeader>

    <p class="text-sm text-tokyo-night-fg-dark">
      Are you sure you want to delete
      <span class="font-semibold text-tokyo-night-fg"
        >"{stream?.name || `Stream ${stream?.id.slice(0, 8)}`}"</span
      >?
    </p>

    <DialogFooter class="gap-2">
      <Button variant="outline" onclick={onCancel} disabled={deleting}>
        Cancel
      </Button>
      <Button variant="destructive" onclick={onConfirm} disabled={deleting}>
        {deleting ? "Deleting..." : "Delete Stream"}
      </Button>
    </DialogFooter>
  </DialogContent>
</Dialog>
