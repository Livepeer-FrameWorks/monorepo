<script lang="ts">
  import { preventDefault } from "svelte/legacy";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Label } from "$lib/components/ui/label";
  import { Checkbox } from "$lib/components/ui/checkbox";
  import { Select, SelectContent, SelectItem, SelectTrigger } from "$lib/components/ui/select";
  import {
    Dialog,
    DialogContent,
    DialogDescription,
    DialogFooter,
    DialogHeader,
    DialogTitle,
  } from "$lib/components/ui/dialog";
  import { getIconComponent } from "$lib/iconUtils";

  interface PushTarget {
    id: string;
    name: string;
    targetUri: string;
    isEnabled: boolean;
    platform?: string | null;
    videoChoice?: string | null;
  }

  let {
    open = $bindable(false),
    target = null,
    loading = false,
    liveVideoAbr = "INHERIT",
    onUpdate,
  }: {
    open: boolean;
    target: PushTarget | null;
    loading: boolean;
    liveVideoAbr?: string;
    onUpdate?: (data: {
      name?: string;
      targetUri?: string;
      isEnabled?: boolean;
      videoChoice?: string;
    }) => void;
  } = $props();

  let name = $state("");
  let targetUri = $state("");
  let isEnabled = $state(true);
  let videoChoice = $state("AUTO");
  let isSrt = $derived(targetUri.trim().toLowerCase().startsWith("srt://"));

  $effect(() => {
    if (open && target) {
      name = target.name;
      targetUri = target.targetUri;
      isEnabled = target.isEnabled;
      videoChoice = target.videoChoice ?? "AUTO";
    }
  });

  let isValid = $derived(name.trim().length > 0 && targetUri.trim().length > 0);

  async function handleSubmit() {
    if (!isValid || !target) return;
    const updates: {
      name?: string;
      targetUri?: string;
      isEnabled?: boolean;
      videoChoice?: string;
    } = {};
    if (name.trim() !== target.name) updates.name = name.trim();
    if (targetUri.trim() !== target.targetUri) updates.targetUri = targetUri.trim();
    if (isEnabled !== target.isEnabled) updates.isEnabled = isEnabled;
    const desiredVideoChoice = isSrt ? "AUTO" : videoChoice;
    if (desiredVideoChoice !== (target.videoChoice ?? "AUTO"))
      updates.videoChoice = desiredVideoChoice;
    if (Object.keys(updates).length > 0) {
      await onUpdate?.(updates);
    } else {
      open = false;
    }
  }
</script>

<Dialog {open} onOpenChange={(value) => (open = value)}>
  <DialogContent
    class="max-w-lg rounded-none border-[hsl(var(--tn-fg-gutter)/0.3)] bg-background p-0 gap-0 overflow-hidden"
  >
    <DialogHeader class="slab-header text-left space-y-1">
      <DialogTitle class="uppercase tracking-wide text-sm font-semibold text-muted-foreground"
        >Edit Push Target</DialogTitle
      >
      <DialogDescription class="text-xs text-muted-foreground/70">
        Update the name, target URI, or toggle this restreaming destination.
      </DialogDescription>
    </DialogHeader>

    <form
      id="edit-push-target-form"
      onsubmit={preventDefault(handleSubmit)}
      class="slab-body--padded space-y-4"
    >
      <div class="space-y-2">
        <Label for="editName" class="text-sm font-medium text-foreground">Name</Label>
        <Input
          id="editName"
          type="text"
          bind:value={name}
          placeholder="e.g., My Twitch Channel"
          required
          class="transition-all focus:ring-2 focus:ring-primary"
        />
      </div>

      <div class="space-y-2">
        <Label for="editUri" class="text-sm font-medium text-foreground">Target URI</Label>
        <Input
          id="editUri"
          type="text"
          bind:value={targetUri}
          placeholder="rtmp://..."
          required
          class="font-mono text-sm transition-all focus:ring-2 focus:ring-primary"
        />
        <p class="text-xs text-muted-foreground/70">
          Supports rtmp://, rtmps://, and srt:// protocols
        </p>
      </div>

      <div class="flex items-start space-x-2">
        {#if !isSrt}
          <div class="w-full space-y-2">
            <Label for="editVideoChoice" class="text-sm font-medium text-foreground"
              >Video track</Label
            >
            <Select
              value={videoChoice}
              onValueChange={(value) => (videoChoice = value)}
              type="single"
            >
              <SelectTrigger id="editVideoChoice" class="w-full">
                {videoChoice === "SOURCE_VIDEO"
                  ? "Source video"
                  : videoChoice === "PROCESSED_VIDEO"
                    ? "Processed video"
                    : "Automatic (recommended)"}
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="AUTO">Automatic (recommended)</SelectItem>
                <SelectItem value="SOURCE_VIDEO">Source video only</SelectItem>
                <SelectItem value="PROCESSED_VIDEO">Processed video only</SelectItem>
              </SelectContent>
            </Select>
            <p class="text-xs text-muted-foreground/70">
              Automatic uses source video when compatible and a rendition when required by the
              destination.
            </p>
            {#if liveVideoAbr === "OFF" && videoChoice === "PROCESSED_VIDEO"}
              <p class="text-xs text-warning">
                Live video ABR is off for this stream, so processed video may be unavailable.
              </p>
            {/if}
          </div>
        {/if}
      </div>

      <div class="flex items-start space-x-2">
        <Checkbox id="editEnabled" bind:checked={isEnabled} />
        <Label for="editEnabled" class="text-sm text-foreground">
          Enabled — automatically push when stream goes live
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
        disabled={loading || !isValid}
        class="rounded-none h-12 flex-1 hover:bg-muted/10 text-primary hover:text-primary/80 gap-2"
        form="edit-push-target-form"
      >
        {#if loading}
          {@const SvelteComponent = getIconComponent("Loader")}
          <SvelteComponent class="w-4 h-4 animate-spin" />
        {/if}
        Save Changes
      </Button>
    </DialogFooter>
  </DialogContent>
</Dialog>
