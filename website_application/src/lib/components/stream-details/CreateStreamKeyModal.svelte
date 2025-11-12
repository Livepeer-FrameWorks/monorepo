<script lang="ts">
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import {
    Dialog,
    DialogContent,
    DialogDescription,
    DialogFooter,
    DialogHeader,
    DialogTitle,
  } from "$lib/components/ui/dialog";

  interface Props {
    open: boolean;
    keyName: string;
    creating: boolean;
    onSubmit: () => void;
    onCancel: () => void;
    onKeyNameChange: (value: string) => void;
  }

  let { open, keyName = $bindable(), creating, onSubmit, onCancel }: Props =
    $props();
</script>

<Dialog
  {open}
  onOpenChange={(value) => {
    if (!value) onCancel();
  }}
>
  <DialogContent class="max-w-md">
    <DialogHeader>
      <DialogTitle>Create Stream Key</DialogTitle>
      <DialogDescription>
        Generate additional keys for alternate encoders or environments.
      </DialogDescription>
    </DialogHeader>

    <div class="space-y-4">
      <div>
        <label
          for="key-name"
          class="block text-sm font-medium text-tokyo-night-fg-dark mb-2"
        >
          Key Name *
        </label>
        <Input
          id="key-name"
          type="text"
          bind:value={keyName}
          placeholder="Production Key"
          class="w-full"
          disabled={creating}
        />
        <p class="text-xs text-tokyo-night-comment mt-1">
          Give your stream key a descriptive name to identify its purpose
        </p>
      </div>
    </div>

    <DialogFooter class="gap-2">
      <Button variant="outline" onclick={onCancel} disabled={creating}>
        Cancel
      </Button>
      <Button onclick={onSubmit} disabled={creating || !keyName.trim()}>
        {creating ? "Creating..." : "Create Key"}
      </Button>
    </DialogFooter>
  </DialogContent>
</Dialog>
