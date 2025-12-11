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

  // Local interface for fields this component needs
  interface DeleteStreamData {
    id: string;
    name?: string | null;
  }

  interface Props {
    open: boolean;
    stream: DeleteStreamData | null;
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

    <p class="text-sm text-muted-foreground">
      Are you sure you want to delete
      <span class="font-semibold text-foreground"
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
