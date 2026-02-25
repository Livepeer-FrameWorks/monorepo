<script lang="ts">
  import { preventDefault } from "svelte/legacy";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Label } from "$lib/components/ui/label";
  import {
    Dialog,
    DialogContent,
    DialogDescription,
    DialogFooter,
    DialogHeader,
    DialogTitle,
  } from "$lib/components/ui/dialog";
  import { Select, SelectContent, SelectItem, SelectTrigger } from "$lib/components/ui/select";
  import { getIconComponent } from "$lib/iconUtils";

  let { open = $bindable(false), loading = false, onCreate } = $props();

  const PLATFORM_PRESETS: Record<
    string,
    { label: string; ingestUrl: string; placeholder: string }
  > = {
    twitch: {
      label: "Twitch",
      ingestUrl: "rtmp://live.twitch.tv/app/",
      placeholder: "live_xxxxxxxxxxxx",
    },
    youtube: {
      label: "YouTube",
      ingestUrl: "rtmp://a.rtmp.youtube.com/live2/",
      placeholder: "xxxx-xxxx-xxxx-xxxx",
    },
    facebook: {
      label: "Facebook",
      ingestUrl: "rtmps://live-api-s.facebook.com:443/rtmp/",
      placeholder: "FB-xxxx...",
    },
    kick: {
      label: "Kick",
      ingestUrl: "rtmps://fa723fc1b171.global-contribute.live-video.net/app/",
      placeholder: "sk_xxxxxxxxxxxx",
    },
    x: {
      label: "X (Twitter)",
      ingestUrl: "rtmps://prod-ec-us-west-2.compose.broadcast.live-video.net:443/rtmp/",
      placeholder: "xxxxxxxxxxxx",
    },
    custom: {
      label: "Custom RTMP/SRT",
      ingestUrl: "",
      placeholder: "rtmp://your-server.com/live/stream-key",
    },
  };

  let platform = $state("twitch");
  let name = $state("");
  let streamKey = $state("");
  let customUri = $state("");

  let preset = $derived(PLATFORM_PRESETS[platform]);
  let isCustom = $derived(platform === "custom");
  let targetUri = $derived(isCustom ? customUri : `${preset.ingestUrl}${streamKey}`);
  let isValid = $derived(
    name.trim().length > 0 && (isCustom ? customUri.trim().length > 0 : streamKey.trim().length > 0)
  );

  $effect(() => {
    if (!open) {
      platform = "twitch";
      name = "";
      streamKey = "";
      customUri = "";
    }
  });

  async function handleSubmit() {
    if (!isValid) return;
    await onCreate?.({
      platform: isCustom ? undefined : platform,
      name: name.trim(),
      targetUri,
    });
  }
</script>

<Dialog {open} onOpenChange={(value) => (open = value)}>
  <DialogContent
    class="max-w-lg rounded-none border-[hsl(var(--tn-fg-gutter)/0.3)] bg-background p-0 gap-0 overflow-hidden"
  >
    <DialogHeader class="slab-header text-left space-y-1">
      <DialogTitle class="uppercase tracking-wide text-sm font-semibold text-muted-foreground"
        >Add Push Target</DialogTitle
      >
      <DialogDescription class="text-xs text-muted-foreground/70">
        Configure an external platform to restream to when your stream goes live.
      </DialogDescription>
    </DialogHeader>

    <form
      id="create-push-target-form"
      onsubmit={preventDefault(handleSubmit)}
      class="slab-body--padded space-y-4"
    >
      <div class="space-y-2">
        <Label for="platform" class="text-sm font-medium text-foreground">Platform</Label>
        <Select
          value={platform}
          onValueChange={(value) => {
            platform = value;
          }}
          type="single"
        >
          <SelectTrigger class="w-full">
            {preset.label}
          </SelectTrigger>
          <SelectContent>
            {#each Object.entries(PLATFORM_PRESETS) as [key, p] (key)}
              <SelectItem value={key}>{p.label}</SelectItem>
            {/each}
          </SelectContent>
        </Select>
      </div>

      <div class="space-y-2">
        <Label for="targetName" class="text-sm font-medium text-foreground">Name</Label>
        <Input
          id="targetName"
          type="text"
          bind:value={name}
          placeholder="e.g., My Twitch Channel"
          required
          class="transition-all focus:ring-2 focus:ring-primary"
        />
      </div>

      {#if isCustom}
        <div class="space-y-2">
          <Label for="customUri" class="text-sm font-medium text-foreground">Target URI</Label>
          <Input
            id="customUri"
            type="text"
            bind:value={customUri}
            placeholder={preset.placeholder}
            required
            class="font-mono text-sm transition-all focus:ring-2 focus:ring-primary"
          />
          <p class="text-xs text-muted-foreground/70">
            Supports rtmp://, rtmps://, and srt:// protocols
          </p>
        </div>
      {:else}
        <div class="space-y-2">
          <Label for="streamKey" class="text-sm font-medium text-foreground">Stream Key</Label>
          <Input
            id="streamKey"
            type="password"
            bind:value={streamKey}
            placeholder={preset.placeholder}
            required
            class="font-mono text-sm transition-all focus:ring-2 focus:ring-primary"
          />
          <p class="text-xs text-muted-foreground/70">
            {preset.ingestUrl}<span class="text-info">&lbrace;your key&rbrace;</span>
          </p>
        </div>
      {/if}
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
        form="create-push-target-form"
      >
        {#if loading}
          {@const SvelteComponent = getIconComponent("Loader")}
          <SvelteComponent class="w-4 h-4 animate-spin" />
        {/if}
        Add Target
      </Button>
    </DialogFooter>
  </DialogContent>
</Dialog>
