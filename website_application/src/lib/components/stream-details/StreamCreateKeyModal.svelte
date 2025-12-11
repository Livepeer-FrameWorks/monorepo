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

  let {
    open = $bindable(false),
    loading = false,
    onCreate,
  } = $props();

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
  <DialogContent class="max-w-md backdrop-blur-sm">
    <DialogHeader>
      <DialogTitle class="gradient-text">Create Stream Key</DialogTitle>
      <DialogDescription>
        Provide a name and choose whether this key should be active immediately.
      </DialogDescription>
    </DialogHeader>

    <form onsubmit={preventDefault(handleSubmit)} class="space-y-4">
      <div class="space-y-2">
        <label
          for="keyName"
          class="block text-sm font-medium text-foreground"
        >
          Key Name
        </label>
        <Input
          id="keyName"
          type="text"
          bind:value={formData.keyName}
          placeholder="e.g., OBS Studio, Mobile App"
          required
          class="transition-all focus:ring-2 focus:ring-primary"
        />
      </div>

      <div class="flex items-start space-x-2 p-3 bg-card/50">
        <Checkbox id="keyActive" bind:checked={formData.isActive} />
        <Label for="keyActive" class="text-sm text-foreground">
          Active - Key can be used immediately after creation
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
          Create Key
        </Button>
      </DialogFooter>
    </form>
  </DialogContent>
</Dialog>
