<script lang="ts">
  import { onMount, untrack } from "svelte";
  import { get } from "svelte/store";
  import { resolve } from "$app/paths";
  import { page } from "$app/state";
  import { replaceState } from "$app/navigation";
  import { fragment, GetStreamsConnectionStore, StreamCoreFieldsStore } from "$houdini";
  import { StreamCrafter } from "@livepeer-frameworks/streamcrafter-svelte";
  import { getDocsSiteUrl, getMarketingSiteUrl } from "$lib/config";
  import { auth } from "$lib/stores/auth";
  import { IngestResolutionError, resolveIngestDestination } from "$lib/placement/ingest-api";
  import { destinationHost, ingestProtocolUrl, type ResolvedIngest } from "$lib/placement/ingest";
  import { resolveOperationalStreamId } from "$lib/route-ids";
  import { getIconComponent } from "$lib/iconUtils";
  import { Button } from "$lib/components/ui/button";
  import { Select, SelectTrigger, SelectContent, SelectItem } from "$lib/components/ui/select";
  import {
    Dialog,
    DialogContent,
    DialogDescription,
    DialogFooter,
    DialogHeader,
    DialogTitle,
  } from "$lib/components/ui/dialog";
  import { Switch } from "$lib/components/ui/switch";
  import { Label } from "$lib/components/ui/label";
  import { toast } from "$lib/stores/toast.js";
  import { themeStore } from "$lib/stores/theme.svelte";
  import { describeResolveFailure, describeStudioFailure, type GoLiveFailure } from "./failures";
  import {
    DEFAULT_STUDIO_SETTINGS,
    RENDERER_LABELS,
    loadStudioSettings,
    saveStudioSettings,
    type CompositorRenderer,
    type StudioSettings,
  } from "./studio-settings";
  import { loadStudioLayout, saveStudioLayout, studioTarget } from "./studio-layout";
  import type { StudioLayout } from "@livepeer-frameworks/streamcrafter-core";

  const streamsStore = new GetStreamsConnectionStore();
  const streamFragmentStore = new StreamCoreFieldsStore();

  const docsSiteUrl = getDocsSiteUrl().replace(/\/$/, "");
  const marketingSiteUrl = getMarketingSiteUrl().replace(/\/$/, "");

  let selectedStreamId = $state<string | null>(null);
  let isStreaming = $state(false);
  let connectionBusy = $state(false);
  // The studio follows the stream it was opened for: its WHIP URL and that
  // stream's saved layout change together once the destination of another
  // stream resolves, which the SDK takes as a stream switch and starts from
  // that layout. Every connection still asks resolveConnectionDestination for
  // the selected stream. Streams cannot be switched while connected, so a live
  // encoder is never rebuilt.
  let studioWhipUrl = $state<string | null>(null);
  let studioLayout = $state.raw<StudioLayout | null>(null);
  let studioStreamId = "";
  let studioIdentity = "";
  let checking = $state(false);
  let destination = $state<ResolvedIngest | null>(null);
  let destinationTime = $state("");
  let destinationEpoch = 0;
  let failure = $state<(GoLiveFailure & { source: "resolve" | "studio" }) | null>(null);
  let lastResolveMessage = "";
  let loadError = $state<string | null>(null);
  let deepLinkNotice = $state<string | null>(null);
  let settings = $state<StudioSettings>({ ...DEFAULT_STUDIO_SETTINGS });
  let settingsDraft = $state<StudioSettings>({ ...DEFAULT_STUDIO_SETTINGS });
  let settingsOpen = $state(false);

  let loading = $derived($streamsStore.fetching);
  let streams = $derived(
    $streamsStore.data?.streamsConnection?.edges
      ?.map((e) => get(fragment(e.node, streamFragmentStore)))
      .filter((stream) => stream?.ingestMode === "PUSH") ?? []
  );
  let selectedStream = $derived(streams.find((s) => s?.id === selectedStreamId) ?? null);
  const identity = $derived(
    $auth.isAuthenticated && $auth.user?.tenant_id ? `${$auth.user.tenant_id}:${$auth.user.id}` : ""
  );

  $effect(() => {
    const current = identity;
    untrack(() => {
      if (current !== studioIdentity) {
        studioIdentity = current;
        studioWhipUrl = null;
        studioLayout = null;
        studioStreamId = "";
      }
    });
  });

  // Look the destination up as soon as a stream is selected, so the studio
  // mounts without an extra step and key or capacity problems show early.
  const destinationBinding = $derived(
    identity && selectedStream?.streamId && selectedStream?.streamKey
      ? `${identity}:${selectedStream.streamId}:${selectedStream.streamKey}`
      : ""
  );
  $effect(() => {
    const ready = destinationBinding !== "";
    untrack(() => {
      destinationEpoch++;
      destination = null;
      destinationTime = "";
      failure = null;
      if (ready) void checkDestination();
    });
  });

  onMount(async () => {
    settings = loadStudioSettings(storage());
    await loadStreams();
  });

  function storage(): Storage | undefined {
    try {
      return window.localStorage;
    } catch {
      return undefined;
    }
  }

  function findStream(param: string) {
    const operationalId = resolveOperationalStreamId({ routeParamId: param });
    return (
      streams.find(
        (s) =>
          s?.id === param ||
          s?.streamId === param ||
          (operationalId && s?.streamId === operationalId)
      ) ?? null
    );
  }

  async function loadStreams() {
    try {
      await streamsStore.fetch();
      loadError = null;
      const requested = page.url.searchParams.get("stream");
      const match = requested ? findStream(requested) : null;
      deepLinkNotice =
        requested && !match
          ? "The linked stream is not available here. Go Live lists streams that receive pushed video."
          : null;
      if (match) {
        selectedStreamId = match.id;
      } else if (!selectedStreamId && streams.length > 0) {
        selectedStreamId = streams[0]?.id ?? null;
      }
    } catch (err) {
      console.error("Failed to load streams:", err);
      loadError = "Failed to load streams";
    }
  }

  function handleStreamSelect(value: string) {
    if (connectionBusy) return;
    selectedStreamId = value;
    deepLinkNotice = null;
    const stream = streams.find((s) => s?.id === value);
    if (stream?.streamId) {
      replaceState(resolve(`/go-live?stream=${encodeURIComponent(stream.streamId)}`), page.state);
    }
  }

  async function resolveConnectionDestination(signal?: AbortSignal): Promise<string> {
    const epoch = destinationEpoch;
    const stream = selectedStream;
    if (!identity || !stream?.streamId || !stream.streamKey)
      throw new Error("Select a stream first.");
    try {
      const result = await resolveIngestDestination(
        stream.streamId,
        stream.streamKey,
        signal,
        "WHIP"
      );
      if (epoch !== destinationEpoch || signal?.aborted)
        throw new DOMException("Resolution cancelled", "AbortError");
      const url = ingestProtocolUrl(result.primary, "whip");
      if (!url) throw new IngestResolutionError("No WHIP endpoint", "PROTOCOL_UNAVAILABLE");
      destination = result;
      destinationTime = new Date().toLocaleTimeString();
      if (failure?.source === "resolve") failure = null;
      return url;
    } catch (error) {
      if (epoch === destinationEpoch && !signal?.aborted && !isAbort(error)) {
        destination = null;
        destinationTime = "";
        lastResolveMessage = error instanceof Error ? error.message : String(error);
        failure = {
          ...(error instanceof IngestResolutionError
            ? describeResolveFailure(error.code, error.reason)
            : describeResolveFailure(null)),
          source: "resolve",
        };
      }
      throw error;
    }
  }

  function isAbort(error: unknown) {
    return error instanceof DOMException && error.name === "AbortError";
  }

  async function checkDestination() {
    const epoch = destinationEpoch;
    checking = true;
    try {
      const url = await resolveConnectionDestination();
      const stream = selectedStream;
      if (epoch === destinationEpoch && stream && !connectionBusy) {
        const next = studioTarget(
          { streamId: studioStreamId, whipUrl: studioWhipUrl, layout: studioLayout },
          stream.id,
          url,
          (id) => loadStudioLayout(storage(), id)
        );
        studioStreamId = next.streamId;
        studioLayout = next.layout;
        studioWhipUrl = next.whipUrl;
      }
    } catch {
      // resolveConnectionDestination records what failed.
    } finally {
      if (epoch === destinationEpoch) checking = false;
    }
  }

  function handleStateChange(state: string) {
    connectionBusy = ["connecting", "streaming", "reconnecting"].includes(state);
    const wasStreaming = isStreaming;
    const nextStreaming = state === "streaming";
    isStreaming = nextStreaming;

    if (!wasStreaming && nextStreaming) {
      failure = null;
      toast.success("You are now live!");
    } else if (wasStreaming && !nextStreaming && state === "idle") {
      toast.info("Broadcast ended");
    }
  }

  function handleError(message: string) {
    if (failure?.source === "resolve" && message === lastResolveMessage) {
      toast.error(failure.title);
      return;
    }
    const described = describeStudioFailure(message);
    failure = { ...described, source: "studio" };
    toast.error(described.title);
  }

  function openSettings() {
    settingsDraft = { ...settings };
    settingsOpen = true;
  }

  function applySettings() {
    const changed =
      settingsDraft.enableCompositor !== settings.enableCompositor ||
      settingsDraft.compositorRenderer !== settings.compositorRenderer;
    settings = { ...settingsDraft };
    saveStudioSettings(storage(), settings);
    settingsOpen = false;
    if (changed && failure?.source === "studio") failure = null;
  }

  function handleLayoutChange(layout: StudioLayout) {
    saveStudioLayout(storage(), studioStreamId, layout);
  }

  const SignalIcon = getIconComponent("Signal");
  const VideoIcon = getIconComponent("Video");
  const RadioIcon = getIconComponent("Radio");
  const AlertCircleIcon = getIconComponent("AlertCircle");
  const PlusIcon = getIconComponent("Plus");
  const SettingsIcon = getIconComponent("Settings");
  const CircleIcon = getIconComponent("Circle");
</script>

<svelte:head>
  <title>Go Live - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col">
  <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0">
    <div class="flex justify-between items-center">
      <div class="flex items-center gap-3">
        <SignalIcon class="w-5 h-5 text-primary" />
        <div>
          <h1 class="text-xl font-bold text-foreground">Go Live</h1>
          <p class="text-sm text-muted-foreground">
            Broadcast from your browser with StreamCrafter
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

  <div class="flex-1 overflow-y-auto flex flex-col relative">
    {#if loading}
      <div class="px-4 sm:px-6 lg:px-8 py-6">
        <div class="flex items-center justify-center min-h-64">
          <div class="loading-spinner w-8 h-8"></div>
        </div>
      </div>
    {:else if loadError && streams.length === 0}
      <div class="px-4 sm:px-6 lg:px-8 py-6">
        <div class="text-center py-12">
          <AlertCircleIcon class="w-6 h-6 text-destructive mx-auto mb-4" />
          <h3 class="text-lg font-semibold text-destructive mb-2">Error Loading Streams</h3>
          <p class="text-muted-foreground mb-6">{loadError}</p>
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
        <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-border/30 bg-muted/10 space-y-3">
          <div class="flex flex-wrap items-center gap-4">
            <span class="text-sm font-medium text-foreground">Broadcast to:</span>
            <Select
              value={selectedStreamId ?? ""}
              onValueChange={handleStreamSelect}
              type="single"
              disabled={connectionBusy}
            >
              <SelectTrigger class="min-w-[250px]" aria-label="Stream to broadcast to">
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

            <Button
              variant="ghost"
              size="icon"
              disabled={connectionBusy}
              onclick={openSettings}
              aria-label="Studio settings"
            >
              <SettingsIcon class="w-5 h-5" />
            </Button>
          </div>

          {#if selectedStream}
            <div class="flex flex-wrap items-center gap-x-6 gap-y-1 text-xs text-muted-foreground">
              <span class="flex items-center gap-1.5" data-testid="recording-status">
                {#if selectedStream.record}
                  <CircleIcon class="w-2 h-2 fill-current text-error" />
                  Recording on: the broadcast is saved to your Library.
                {:else}
                  <CircleIcon class="w-2 h-2" />
                  Recording off.
                  <a
                    href={resolve("/streams/[id]", { id: selectedStream.id })}
                    class="text-primary hover:underline">Turn it on in stream settings</a
                  >
                {/if}
              </span>
              {#if destination}
                <span class="break-all">
                  Ingest node: {destinationHost(destination.primary)} · {destination.primary
                    .region ?? "region unknown"} · checked {destinationTime}
                </span>
              {:else if checking}
                <span>Finding an ingest node…</span>
              {/if}
            </div>
          {/if}

          {#if deepLinkNotice}
            <p class="text-xs text-warning" role="status">{deepLinkNotice}</p>
          {/if}

          {#if failure && selectedStream}
            <div
              role="alert"
              class="border-l-2 border-destructive bg-destructive/5 p-3 text-sm space-y-2"
            >
              <p class="font-semibold text-destructive">{failure.title}</p>
              <p class="text-muted-foreground">{failure.detail}</p>
              {#if failure.actions.length > 0}
                <div class="flex flex-wrap gap-2">
                  {#each failure.actions as action (action)}
                    {#if action === "keys"}
                      <Button
                        size="sm"
                        variant="outline"
                        href={resolve("/streams/[id]", { id: selectedStream.id })}
                        >Open stream keys</Button
                      >
                    {:else if action === "billing"}
                      <Button size="sm" variant="outline" href={resolve("/account/billing")}
                        >Open billing</Button
                      >
                    {:else if action === "login"}
                      <Button size="sm" variant="outline" href={resolve("/login")}>Sign in</Button>
                    {:else if action === "settings"}
                      <Button size="sm" variant="outline" onclick={openSettings}
                        >Studio settings</Button
                      >
                    {:else if action === "retry"}
                      <Button
                        size="sm"
                        variant="outline"
                        disabled={checking || connectionBusy}
                        onclick={checkDestination}>Try again</Button
                      >
                    {/if}
                  {/each}
                </div>
              {/if}
            </div>
          {/if}
        </div>

        <div class="flex-1 bg-black relative flex flex-col">
          {#if selectedStream && studioWhipUrl && identity}
            <div class="w-full min-h-full flex flex-col">
              <StreamCrafter
                whipUrl={studioWhipUrl}
                resolveWhipUrl={resolveConnectionDestination}
                initialProfile="broadcast"
                autoStartCamera={false}
                showSettings={false}
                theme={themeStore.playerTheme as import("@livepeer-frameworks/streamcrafter-core").FwThemePreset}
                enableCompositor={settings.enableCompositor}
                compositorConfig={{ renderer: settings.compositorRenderer }}
                initialLayout={studioLayout}
                onLayoutChange={handleLayoutChange}
                onStateChange={handleStateChange}
                onError={handleError}
                class="w-full flex-1"
              />
            </div>
          {:else}
            <div class="flex items-center justify-center h-full min-h-64 text-muted-foreground">
              <div class="text-center">
                <VideoIcon class="w-12 h-12 mx-auto mb-4 opacity-50" />
                <p>
                  {#if !selectedStream}
                    Select a stream to start broadcasting
                  {:else if checking}
                    Opening the studio…
                  {:else}
                    The studio opens once an ingest node is found for this stream
                  {/if}
                </p>
              </div>
            </div>
          {/if}
        </div>

        {#if selectedStream}
          <div class="px-4 sm:px-6 lg:px-8 py-4 border-t border-border/30 bg-muted/10">
            <div class="grid grid-cols-1 md:grid-cols-3 gap-4 text-sm">
              <div>
                <h4 class="font-medium text-foreground mb-1">Add sources</h4>
                <p class="text-muted-foreground">
                  Add Camera starts your camera and microphone. Share Screen picks a screen, window
                  or tab. Add media file plays a video or audio file from your computer in the mix.
                </p>
              </div>
              <div>
                <h4 class="font-medium text-foreground mb-1">Arrange the picture</h4>
                <p class="text-muted-foreground">
                  With two or more sources, pick a layout on the preview. The panel under the
                  preview holds scenes, transitions, logos and text.
                </p>
              </div>
              <div>
                <h4 class="font-medium text-foreground mb-1">Go live</h4>
                <p class="text-muted-foreground">
                  Nothing is public until you click Go Live. Keep this tab open for the whole
                  broadcast. Multistream destinations on the stream start and stop with it.
                </p>
              </div>
            </div>

            <!-- eslint-disable svelte/no-navigation-without-resolve -->
            <div
              class="mt-4 pt-4 border-t border-border/30 text-sm text-muted-foreground space-y-1"
            >
              <p>
                New to the studio? The
                <a
                  href={`${docsSiteUrl}/builders/go-live-browser`}
                  target="_blank"
                  rel="noopener noreferrer"
                  class="text-primary hover:underline">Go Live guide</a
                >
                walks through a first broadcast. To add StreamCrafter to your own app, see the
                <a
                  href={`${docsSiteUrl}/builders/ingest`}
                  target="_blank"
                  rel="noopener noreferrer"
                  class="text-primary hover:underline">StreamCrafter SDK docs</a
                >. The
                <a
                  href={`${marketingSiteUrl}/stream-test`}
                  target="_blank"
                  rel="noopener noreferrer"
                  class="text-primary hover:underline">stream test page</a
                >
                publishes to a WHIP URL and plays a playback ID or an HLS, DASH, MP4, WebM or WHEP URL,
                without an account.
              </p>
              <p>
                StreamCrafter is in beta and changes quickly.
                <a
                  href={`${marketingSiteUrl}/showcase#composition`}
                  target="_blank"
                  rel="noopener noreferrer"
                  class="text-primary hover:underline">Cloud production</a
                > is up next.
              </p>
            </div>
            <!-- eslint-enable svelte/no-navigation-without-resolve -->
          </div>
        {/if}
      </div>
    {/if}
  </div>
</div>

<Dialog bind:open={settingsOpen}>
  <DialogContent>
    <DialogHeader>
      <DialogTitle>Studio settings</DialogTitle>
      <DialogDescription>
        Changes apply right away. Sources, scenes and overlays stay in place.
      </DialogDescription>
    </DialogHeader>
    <div class="grid gap-4 py-4">
      <div class="flex items-center justify-between space-x-2">
        <div>
          <Label for="compositor-mode">Compositor</Label>
          <p class="text-xs text-muted-foreground">
            Mixes several sources with layouts, scenes and overlays. Turn it off to send one source
            at a time.
          </p>
        </div>
        <Switch
          id="compositor-mode"
          checked={settingsDraft.enableCompositor}
          onCheckedChange={(v) => (settingsDraft.enableCompositor = v)}
        />
      </div>
      <div class="grid gap-2">
        <Label for="renderer-engine">Renderer</Label>
        <Select
          value={settingsDraft.compositorRenderer}
          onValueChange={(v) => (settingsDraft.compositorRenderer = v as CompositorRenderer)}
          type="single"
          disabled={!settingsDraft.enableCompositor}
        >
          <SelectTrigger id="renderer-engine">
            {RENDERER_LABELS[settingsDraft.compositorRenderer]}
          </SelectTrigger>
          <SelectContent>
            {#each Object.entries(RENDERER_LABELS) as [value, label] (value)}
              <SelectItem {value}>{label}</SelectItem>
            {/each}
          </SelectContent>
        </Select>
        <p class="text-xs text-muted-foreground">
          Auto picks the fastest renderer your browser supports.
        </p>
      </div>
    </div>
    <DialogFooter>
      <Button variant="ghost" onclick={() => (settingsOpen = false)}>Cancel</Button>
      <Button disabled={connectionBusy} onclick={applySettings}>Apply</Button>
    </DialogFooter>
  </DialogContent>
</Dialog>
