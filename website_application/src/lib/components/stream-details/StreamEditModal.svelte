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
  import { getIconComponent } from "$lib/iconUtils";

  let {
    open = $bindable(false),
    stream,
    loading = false,
    onSave,
  } = $props();

  let formData = $state({
    name: stream?.name || "",
    description: stream?.description || "",
    record: stream?.record || false,
  });

  // Reset form when stream changes
  $effect(() => {
    if (stream) {
      formData = {
        name: stream.name || "",
        description: stream.description || "",
        record: stream.record || false,
      };
    }
  });

  async function handleSubmit() {
    await onSave?.(formData);
  }
</script>

<Dialog {open} onOpenChange={(value) => (open = value)}>
  <DialogContent class="max-w-md backdrop-blur-sm">
    <DialogHeader>
      <DialogTitle class="gradient-text">Edit Stream</DialogTitle>
      <DialogDescription>
        Update the name, description, or recording preferences for this stream.
      </DialogDescription>
    </DialogHeader>

    <form onsubmit={preventDefault(handleSubmit)} class="space-y-4">
      <div class="space-y-2">
        <label
          for="editName"
          class="block text-sm font-medium text-foreground"
        >
          Stream Name
        </label>
        <Input
          id="editName"
          type="text"
          bind:value={formData.name}
          required
          class="transition-all focus:ring-2 focus:ring-primary"
        />
      </div>

      <div class="space-y-2">
        <label
          for="editDescription"
          class="block text-sm font-medium text-foreground"
        >
          Description
        </label>
        <Textarea
          id="editDescription"
          bind:value={formData.description}
          rows={3}
          class="transition-all focus:ring-2 focus:ring-primary"
        />
      </div>

      <div class="flex items-start space-x-2">
        <Checkbox id="editRecord" bind:checked={formData.record} />
        <Label for="editRecord" class="text-sm text-foreground">
          Enable Recording
        </Label>
      </div>

      <DialogFooter class="gap-2">
        <Button type="button" variant="outline" onclick={() => (open = false)}>
          Cancel
        </Button>
        <Button
          type="submit"
          disabled={loading}
          class="gap-2 transition-all hover:shadow-brand-soft"
        >
          {#if loading}
            {@const SvelteComponent = getIconComponent("Loader")}
            <SvelteComponent class="w-4 h-4 animate-spin" />
          {/if}
          Save Changes
        </Button>
      </DialogFooter>
    </form>
  </DialogContent>
</Dialog>
