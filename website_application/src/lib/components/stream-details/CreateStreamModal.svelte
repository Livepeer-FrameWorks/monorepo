<script lang="ts">
  import { preventDefault } from "svelte/legacy";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Textarea } from "$lib/components/ui/textarea";
  import { Checkbox } from "$lib/components/ui/checkbox";
  import { Label } from "$lib/components/ui/label";
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
    title: string;
    description: string;
    record: boolean;
    creating: boolean;
    onSubmit: () => void;
    onCancel: () => void;
  }

  let {
    open,
    title = $bindable(),
    description = $bindable(),
    record = $bindable(),
    creating,
    onSubmit,
    onCancel,
  }: Props = $props();
</script>

<Dialog
  {open}
  onOpenChange={(value) => {
    if (!value) onCancel();
  }}
>
  <DialogContent class="max-w-md rounded-none border-[hsl(var(--tn-fg-gutter)/0.3)] bg-background p-0 gap-0 overflow-hidden">
    <DialogHeader class="slab-header text-left space-y-1">
      <DialogTitle class="uppercase tracking-wide text-sm font-semibold text-muted-foreground">Create New Stream</DialogTitle>
      <DialogDescription class="text-xs text-muted-foreground/70">
        Configure the basics for your next broadcast.
      </DialogDescription>
    </DialogHeader>

    <form id="create-stream-form" class="slab-body--padded space-y-4" onsubmit={preventDefault(onSubmit)}>
      <div>
        <label
          for="stream-title"
          class="block text-sm font-medium text-muted-foreground mb-2"
        >
          Stream Title *
        </label>
        <Input
          id="stream-title"
          type="text"
          bind:value={title}
          placeholder="My Awesome Stream"
          class="w-full"
          disabled={creating}
          required
        />
      </div>

      <div>
        <label
          for="stream-description"
          class="block text-sm font-medium text-muted-foreground mb-2"
        >
          Description (Optional)
        </label>
        <Textarea
          id="stream-description"
          bind:value={description}
          placeholder="Description of your stream..."
          class="h-20"
          disabled={creating}
        />
      </div>

      <div class="flex items-start space-x-3">
        <Checkbox
          id="create-stream-record"
          bind:checked={record}
          disabled={creating}
        />
        <div>
          <Label
            for="create-stream-record"
            class="text-sm font-medium text-foreground"
          >
            Enable Recording
          </Label>
          <p class="text-xs text-muted-foreground">
            Automatically record your stream to create VOD content
          </p>
        </div>
      </div>
    </form>

    <DialogFooter class="slab-actions slab-actions--row gap-0">
      <Button
        type="button"
        variant="ghost"
        class="rounded-none h-12 flex-1 border-r border-[hsl(var(--tn-fg-gutter)/0.3)] hover:bg-muted/10 text-muted-foreground hover:text-foreground"
        onclick={onCancel}
        disabled={creating}
      >
        Cancel
      </Button>
      <Button 
        type="submit" 
        variant="ghost"
        class="rounded-none h-12 flex-1 hover:bg-muted/10 text-primary hover:text-primary/80"
        disabled={creating || !title.trim()}
        form="create-stream-form"
      >
        {creating ? "Creating..." : "Create Stream"}
      </Button>
    </DialogFooter>
  </DialogContent>
</Dialog>
