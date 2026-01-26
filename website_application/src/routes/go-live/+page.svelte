<script lang="ts">
  import { onMount } from "svelte";
  import { get } from "svelte/store";
  import { resolve } from "$app/paths";
  import { fragment, GetStreamsConnectionStore, StreamCoreFieldsStore } from "$houdini";
  import { StreamCrafter } from "@livepeer-frameworks/streamcrafter-svelte";
  import { getIngestUrls, getDocsSiteUrl } from "$lib/config";
  import { getIconComponent } from "$lib/iconUtils";
  import { Button } from "$lib/components/ui/button";
  import {
    Select,
    SelectTrigger,
    SelectContent,
    SelectItem,
  } from "$lib/components/ui/select";
  import {
    Dialog,
    DialogContent,
    DialogHeader,
    DialogTitle,
    DialogTrigger,
  } from "$lib/components/ui/dialog";
  import { Switch } from "$lib/components/ui/switch";
  import { Label } from "$lib/components/ui/label";
  import { toast } from "$lib/stores/toast.js";

  // Houdini stores
  const streamsStore = new GetStreamsConnectionStore();
  const streamFragmentStore = new StreamCoreFieldsStore();

  // State
  let selectedStreamId = $state<string | null>(null);
  let isStreaming = $state(false);
  let error = $state<string | null>(null);
  let advancedSettings = $state({
    enableCompositor: true,
    compositorRenderer: 'auto' as 'auto' | 'webgl' | 'webgpu' | 'canvas2d',
  });

  // Derived state
  let loading = $derived($streamsStore.fetching);
  let streams = $derived(
    $streamsStore.data?.streamsConnection?.edges?.map((e) => {
      const node = get(fragment(e.node, streamFragmentStore));
      return node;
    }) ?? []
  );
  let selectedStream = $derived(
    streams.find((s) => s?.id === selectedStreamId) ?? null
  );
  let whipUrl = $derived.by(() => {
    if (!selectedStream?.streamKey) return null;
    const urls = getIngestUrls(selectedStream.streamKey);
    return urls.whip ?? null;
  });

  onMount(async () => {
    await loadStreams();
  });

  async function loadStreams() {
    try {
      await streamsStore.fetch();
      // Auto-select first stream if available
      if (!selectedStreamId && streams.length > 0) {
        selectedStreamId = streams[0]?.id ?? null;
      }
    } catch (err) {
      console.error("Failed to load streams:", err);
      error = "Failed to load streams";
    }
  }

  function handleStreamSelect(value: string) {
    selectedStreamId = value;
  }

  function handleStateChange(state: string, context?: unknown) {
    console.debug("[StreamCrafter] State:", state, context);
    isStreaming = state === "streaming";

    if (state === "streaming") {
      toast.success("You are now live!");
    } else if (state === "idle" && isStreaming) {
      toast.info("Broadcast ended");
    }
  }

  function handleError(errorMsg: string) {
    console.error("[StreamCrafter] Error:", errorMsg);
    toast.error(errorMsg);
    error = errorMsg;
  }

  // Icons
  const GlobeIcon = getIconComponent("Globe");
  const VideoIcon = getIconComponent("Video");
  const RadioIcon = getIconComponent("Radio");
  const AlertCircleIcon = getIconComponent("AlertCircle");
  const PlusIcon = getIconComponent("Plus");
  const SettingsIcon = getIconComponent("Settings");
</script>

<svelte:head>
  <title>Go Live - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col">
  <!-- Fixed Page Header -->
  <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0">
    <div class="flex justify-between items-center">
      <div class="flex items-center gap-3">
        <GlobeIcon class="w-5 h-5 text-primary" />
        <div>
          <h1 class="text-xl font-bold text-foreground">Go Live</h1>
          <p class="text-sm text-muted-foreground">
            Stream directly from your browser with WebRTC
          </p>
        </div>
      </div>
      <div class="flex items-center gap-3">
        {#if isStreaming}
          <span class="flex items-center gap-2 text-sm font-medium text-destructive">
            <span class="w-2 h-2 rounded-full bg-destructive animate-pulse"></span>
            LIVE
          </span>
        {/if}
      </div>
    </div>
  </div>

  <!-- Scrollable Content -->
  <div class="flex-1 overflow-y-auto flex flex-col relative">
    {#if loading}
      <div class="px-4 sm:px-6 lg:px-8 py-6">
        <div class="flex items-center justify-center min-h-64">
          <div class="loading-spinner w-8 h-8"></div>
        </div>
      </div>
    {:else if error && streams.length === 0}
      <div class="px-4 sm:px-6 lg:px-8 py-6">
        <div class="text-center py-12">
          <AlertCircleIcon class="w-6 h-6 text-destructive mx-auto mb-4" />
          <h3 class="text-lg font-semibold text-destructive mb-2">Error Loading Streams</h3>
          <p class="text-muted-foreground mb-6">{error}</p>
          <Button onclick={loadStreams}>Try Again</Button>
        </div>
      </div>
    {:else if streams.length === 0}
      <div class="px-4 sm:px-6 lg:px-8 py-6">
        <div class="text-center py-12">
          <VideoIcon class="w-12 h-12 text-muted-foreground mx-auto mb-4" />
          <h3 class="text-lg font-semibold text-foreground mb-2">No Streams Yet</h3>
          <p class="text-muted-foreground mb-6">
            Create a stream first to start broadcasting from your browser.
          </p>
          <Button href={resolve("/streams")} class="gap-2">
            <PlusIcon class="w-4 h-4" />
            Create Stream
          </Button>
        </div>
      </div>
    {:else}
      <div class="page-transition min-h-full flex flex-col">
        <!-- Stream Selector & Settings -->
        <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-border/30 bg-muted/10">
          <div class="flex items-center gap-4">
            <span class="text-sm font-medium text-foreground">Broadcast to:</span>
            <Select value={selectedStreamId ?? ""} onValueChange={handleStreamSelect} type="single">
              <SelectTrigger class="min-w-[250px]">
                {selectedStream?.name ?? "Select a stream..."}
              </SelectTrigger>
              <SelectContent>
                {#each streams as stream (stream?.id)}
                  {#if stream}
                    <SelectItem value={stream.id}>
                      <div class="flex items-center gap-2">
                        <RadioIcon class="w-4 h-4 text-muted-foreground" />
                        {stream.name}
                      </div>
                    </SelectItem>
                  {/if}
                {/each}
              </SelectContent>
            </Select>
            <Button href={resolve("/streams")} variant="outline" size="sm" class="gap-2">
              <PlusIcon class="w-4 h-4" />
              New Stream
            </Button>
            
            <div class="flex-1"></div>

            <!-- Advanced Settings Dialog -->
            <Dialog>
              <DialogTrigger>
                {#snippet child({ props })}
                  <Button variant="ghost" size="icon" {...props}>
                    <SettingsIcon class="w-5 h-5" />
                  </Button>
                {/snippet}
              </DialogTrigger>
              <DialogContent>
                <DialogHeader>
                  <DialogTitle>Advanced Settings</DialogTitle>
                </DialogHeader>
                <div class="grid gap-4 py-4">
                  <div class="flex items-center justify-between space-x-2">
                    <Label for="compositor-mode">Enable Compositor</Label>
                    <Switch
                      id="compositor-mode"
                      checked={advancedSettings.enableCompositor}
                      onCheckedChange={(v) => advancedSettings.enableCompositor = v}
                    />
                  </div>
                  <div class="grid gap-2">
                    <Label for="renderer-engine">Renderer Engine</Label>
                    <Select
                      value={advancedSettings.compositorRenderer}
                      onValueChange={(v) => advancedSettings.compositorRenderer = v as 'auto' | 'webgl' | 'webgpu' | 'canvas2d'}
                      type="single"
                    >
                      <SelectTrigger id="renderer-engine">
                        {advancedSettings.compositorRenderer}
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="auto">Auto</SelectItem>
                        <SelectItem value="webgl">WebGL</SelectItem>
                        <SelectItem value="webgpu">WebGPU</SelectItem>
                        <SelectItem value="canvas2d">Canvas2D</SelectItem>
                      </SelectContent>
                    </Select>
                    <p class="text-xs text-muted-foreground">
                      Controls the rendering engine used for video processing. Auto is recommended.
                    </p>
                  </div>
                </div>
              </DialogContent>
            </Dialog>
          </div>
        </div>

        <!-- StreamCrafter Component -->
        <div class="flex-1 bg-black relative flex flex-col">
          {#if selectedStream && whipUrl}
            {#key `${advancedSettings.enableCompositor}-${advancedSettings.compositorRenderer}`}
              <div class="w-full min-h-full flex flex-col">
                <StreamCrafter
                  whipUrl={whipUrl}
                  initialProfile="broadcast"
                  autoStartCamera={false}
                  showSettings={false}
                  devMode={true}
                  debug={false}
                  enableCompositor={advancedSettings.enableCompositor}
                  compositorConfig={{ renderer: advancedSettings.compositorRenderer }}
                  onStateChange={handleStateChange}
                  onError={handleError}
                  class="w-full flex-1"
                />
              </div>
            {/key}
          {:else}
            <div class="flex items-center justify-center h-full text-muted-foreground">
              <div class="text-center">
                <VideoIcon class="w-12 h-12 mx-auto mb-4 opacity-50" />
                <p>Select a stream to start broadcasting</p>
              </div>
            </div>
          {/if}
        </div>

        <!-- Help Section -->
        {#if selectedStream}
          <div class="px-4 sm:px-6 lg:px-8 py-4 border-t border-border/30 bg-muted/10">
            <div class="grid grid-cols-1 md:grid-cols-3 gap-4 text-sm">
              <div>
                <h4 class="font-medium text-foreground mb-1">Sources & Mixing</h4>
                <p class="text-muted-foreground">
                  Click the camera/mic icons to enable your devices. Grant browser permissions when prompted.
                </p>
              </div>
              <div>
                <h4 class="font-medium text-foreground mb-1">Content Ingest</h4>
                <p class="text-muted-foreground">
                  Share your screen, a window, or a browser tab. Great for presentations and tutorials.
                </p>
              </div>
              <div>
                <h4 class="font-medium text-foreground mb-1">Start Streaming</h4>
                <p class="text-muted-foreground">
                  Click the Go Live button to start streaming. Your broadcast will be available immediately.
                </p>
              </div>
            </div>
            
            <div class="mt-4 pt-4 border-t border-border/30 text-sm text-muted-foreground">
              <strong>Embed StreamCrafter:</strong> You can drop this component into your website and even for non-FrameWorks media servers.
              <a href={resolve(`${getDocsSiteUrl()}/streamers/ingest`)} target="_blank" rel="noopener noreferrer" class="text-primary hover:underline">Read the Docs</a>
            </div>
          </div>
        {/if}
      </div>
    {/if}
  </div>
</div>
