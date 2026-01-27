<script lang="ts">
  import { preventDefault } from "svelte/legacy";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
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

  let { open = $bindable(false), loading = false, onCreate } = $props();

  let formData = $state({
    keyName: "",
    isActive: true,
  });

  // Reset form when modal closes
  $effect(() => {
    if (!open) {
      formData = {
        keyName: "",
        isActive: true,
      };
    }
  });

  async function handleSubmit() {
    await onCreate?.(formData);
  }
</script>

<Dialog {open} onOpenChange={(value) => (open = value)}>
  <DialogContent
    class="max-w-md rounded-none border-[hsl(var(--tn-fg-gutter)/0.3)] bg-background p-0 gap-0 overflow-hidden"
  >
    <DialogHeader class="slab-header text-left space-y-1">
      <DialogTitle class="uppercase tracking-wide text-sm font-semibold text-muted-foreground"
        >Create Stream Key</DialogTitle
      >
      <DialogDescription class="text-xs text-muted-foreground/70">
        Provide a name and choose whether this key should be active immediately.
      </DialogDescription>
    </DialogHeader>

    <form
      id="create-key-form"
      onsubmit={preventDefault(handleSubmit)}
      class="slab-body--padded space-y-4"
    >
      <div class="space-y-2">
        <label for="keyName" class="block text-sm font-medium text-foreground"> Key Name </label>
        <Input
          id="keyName"
          type="text"
          bind:value={formData.keyName}
          placeholder="e.g., OBS Studio, Mobile App"
          required
          class="transition-all focus:ring-2 focus:ring-primary"
        />
      </div>

      <div class="flex items-start space-x-2">
        <Checkbox id="keyActive" bind:checked={formData.isActive} />
        <Label for="keyActive" class="text-sm text-foreground">
          Active - Key can be used immediately after creation
        </Label>
      </div>
    </form>

    <DialogFooter class="slab-actions slab-actions--row gap-0">
      <Button
        type="button"
        variant="ghost"
        class="rounded-none h-12 flex-1 border-r border-[hsl(var(--tn-fg-gutter)/0.3)] hover:bg-muted/10 text-muted-foreground hover:text-foreground"
        onclick={() => (open = false)}
      >
        Cancel
      </Button>
      <Button
        type="submit"
        variant="ghost"
        disabled={loading}
        class="rounded-none h-12 flex-1 hover:bg-muted/10 text-primary hover:text-primary/80 gap-2"
        form="create-key-form"
      >
        {#if loading}
          {@const SvelteComponent = getIconComponent("Loader")}
          <SvelteComponent class="w-4 h-4 animate-spin" />
        {/if}
        Create Key
      </Button>
    </DialogFooter>
  </DialogContent>
</Dialog>
