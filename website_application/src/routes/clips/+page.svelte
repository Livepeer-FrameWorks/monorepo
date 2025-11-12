<script>
  import { preventDefault } from "svelte/legacy";

  import { onMount } from "svelte";
  import { auth } from "$lib/stores/auth";
  import { streamsService } from "$lib/graphql/services/streams.js";
  import { clipsService } from "$lib/graphql/services/clips.js";
  import { toast } from "$lib/stores/toast.js";
  import LoadingCard from "$lib/components/LoadingCard.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import ClipModal from "$lib/components/ClipModal.svelte";
  import { Button } from "$lib/components/ui/button";
  import {
    Dialog,
    DialogContent,
    DialogDescription,
    DialogFooter,
    DialogHeader,
    DialogTitle,
  } from "$lib/components/ui/dialog";
  import { Input } from "$lib/components/ui/input";
  import { Textarea } from "$lib/components/ui/textarea";
  import {
    Select,
    SelectContent,
    SelectItem,
    SelectTrigger,
  } from "$lib/components/ui/select";
  import { getIconComponent } from "$lib/iconUtils";

  let isAuthenticated = false;
  let loading = $state(true);

  // Data
  let streams = $state([]);
  let clips = $state([]);
  let streamsError = $state(false);

  // Clip creation
  let showCreateModal = $state(false);
  let creatingClip = $state(false);
  let selectedStreamId = $state("");
  const selectedStreamLabel = $derived(() => {
    if (!selectedStreamId) {
      return "Select a stream";
    }
    const match = streams.find((stream) => stream.id === selectedStreamId);
    return match?.name || "Select a stream";
  });
  let clipTitle = $state("");
  let clipDescription = $state("");
  let startTime = $state(0);
  let endTime = $state(300); // 5 minutes default

  // Clip viewing
  let selectedClip = $state(null);

  // Subscribe to auth store
  auth.subscribe((authState) => {
    isAuthenticated = authState.isAuthenticated;
  });

  onMount(async () => {
    if (!isAuthenticated) {
      await auth.checkAuth();
    }
    await loadData();
  });

  async function loadData() {
    try {
      loading = true;
      streamsError = false;
      streams = await streamsService.getStreams();
      clips = await clipsService.getClips(); // Load all clips
    } catch (error) {
      console.error("Failed to load data:", error);
      streamsError = true;
      toast.error("Failed to load clips data. Please refresh the page.");
    } finally {
      loading = false;
    }
  }

  async function createClip() {
    if (!clipTitle.trim() || !selectedStreamId) {
      toast.warning("Please fill in all required fields");
      return;
    }

    if (endTime <= startTime) {
      toast.warning("End time must be after start time");
      return;
    }

    try {
      creatingClip = true;

      const newClip = await clipsService.createClip({
        streamId: selectedStreamId,
        title: clipTitle.trim(),
        description: clipDescription.trim() || undefined,
        startTime: Math.floor(startTime),
        endTime: Math.floor(endTime),
      });

      // Add to clips array
      clips = [...clips, newClip];

      // Reset form
      showCreateModal = false;
      clipTitle = "";
      clipDescription = "";
      selectedStreamId = "";
      startTime = 0;
      endTime = 300;

      toast.success("Clip created successfully!");
    } catch (error) {
      console.error("Failed to create clip:", error);
      toast.error("Failed to create clip. Please try again.");
    } finally {
      creatingClip = false;
    }
  }

  function formatDuration(seconds) {
    const minutes = Math.floor(seconds / 60);
    const remainingSeconds = seconds % 60;
    return `${minutes}:${remainingSeconds.toString().padStart(2, "0")}`;
  }

  function formatDate(dateString) {
    return new Date(dateString).toLocaleDateString();
  }

  function getStreamName(streamId) {
    const stream = streams.find((s) => s.id === streamId);
    return stream ? stream.name : streamId || "Unknown Stream";
  }

  function openClip(clip) {
    // Add stream name to clip for modal display
    selectedClip = {
      ...clip,
      streamName: getStreamName(clip.streamId || clip.stream),
    };
  }

  function closeClip() {
    selectedClip = null;
  }
</script>

<svelte:head>
  <title>Clips - FrameWorks</title>
</svelte:head>

<div class="min-h-screen bg-tokyo-night-bg text-tokyo-night-fg">
  <div class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
    <div class="flex items-center justify-between mb-8">
      <div>
        <h1 class="text-3xl font-bold text-tokyo-night-blue mb-2">
          Clips Management
        </h1>
        <p class="text-tokyo-night-comment">
          Create and manage video clips from your streams
        </p>
      </div>
      {#if streamsError}
        <Button
          variant="destructive"
          onclick={loadData}
          title="Failed to load streams. Click to retry."
        >
          Retry Loading
        </Button>
      {:else if streams.length === 0 && !loading}
        <Button
          variant="outline"
          disabled
          title="No streams available. Create a stream first to make clips."
          class="cursor-not-allowed opacity-60"
        >
          Create Clip
        </Button>
      {:else if !loading}
        <Button onclick={() => (showCreateModal = true)}>Create Clip</Button>
      {/if}
    </div>

    {#if loading}
      <!-- Loading skeleton for clips grid -->
      <div class="bg-tokyo-night-surface rounded-lg p-6">
        <div class="skeleton-text-lg w-24 mb-4"></div>
        <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
          {#each Array(6) as _, index (index)}
            <LoadingCard variant="clip" />
          {/each}
        </div>
      </div>
    {:else}
      <!-- Clips Grid -->
      <div class="bg-tokyo-night-surface rounded-lg p-6">
        <h2 class="text-xl font-semibold mb-4 text-tokyo-night-cyan">
          Your Clips
        </h2>

        {#if clips.length === 0}
          <EmptyState
            title="No clips yet"
            description="Create your first clip from a stream to get started"
            actionText={streams.length > 0 ? "Create Your First Clip" : ""}
            onAction={() => (showCreateModal = true)}
            showAction={streams.length > 0}
          >
            {@const SvelteComponent = getIconComponent("Scissors")}
            <SvelteComponent
              class="w-12 h-12 text-tokyo-night-fg-dark mx-auto mb-4"
            />
            {#if streams.length === 0}
              <p class="text-tokyo-night-comment text-sm mt-2">
                You need at least one stream to create clips
              </p>
            {/if}
          </EmptyState>
        {:else}
          <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
            {#each clips as clip (clip.id ?? clip.playbackId)}
              {@const SvelteComponent_1 = getIconComponent("Play")}
              <div
                class="bg-tokyo-night-bg rounded-lg p-4 border border-tokyo-night-selection"
              >
                <div class="mb-3">
                  <h3 class="font-semibold text-lg mb-1">{clip.title}</h3>
                  <p class="text-sm text-tokyo-night-comment">
                    From: {getStreamName(clip.stream)}
                  </p>
                </div>

                {#if clip.description}
                  <p class="text-sm text-tokyo-night-comment mb-3 line-clamp-2">
                    {clip.description}
                  </p>
                {/if}

                <div class="space-y-2 text-sm">
                  <div class="flex justify-between">
                    <span class="text-tokyo-night-comment">Duration</span>
                    <span>{formatDuration(clip.duration)}</span>
                  </div>
                  <div class="flex justify-between">
                    <span class="text-tokyo-night-comment">Start Time</span>
                    <span>{formatDuration(clip.startTime)}</span>
                  </div>
                  <div class="flex justify-between">
                    <span class="text-tokyo-night-comment">Status</span>
                    <span
                      class="px-2 py-1 text-xs rounded-full bg-tokyo-night-blue bg-opacity-20 text-tokyo-night-blue"
                    >
                      {clip.status}
                    </span>
                  </div>
                  <div class="flex justify-between">
                    <span class="text-tokyo-night-comment">Created</span>
                    <span>{formatDate(clip.createdAt)}</span>
                  </div>
                </div>

                <div class="mt-4 pt-4 border-t border-tokyo-night-selection">
                  <Button
                    variant="ghost"
                    size="sm"
                    class="px-0 text-tokyo-night-cyan hover:text-primary transition-colors"
                    onclick={() => openClip(clip)}
                  >
                    <SvelteComponent_1 class="w-4 h-4" />
                    <span>Play Clip</span>
                  </Button>
                </div>
              </div>
            {/each}
          </div>
        {/if}
      </div>
    {/if}
  </div>
</div>

<!-- Create Clip Modal -->
<Dialog
  open={showCreateModal}
  onOpenChange={(value) => (showCreateModal = value)}
>
  <DialogContent class="max-w-lg">
    <form class="space-y-6" onsubmit={preventDefault(createClip)}>
      <DialogHeader>
        <DialogTitle>Create New Clip</DialogTitle>
        <DialogDescription>
          Choose a stream and time range to generate a clip.
        </DialogDescription>
      </DialogHeader>

      <div class="space-y-4">
        <div class="space-y-2">
          <label
            for="stream-select"
            class="text-sm font-medium text-tokyo-night-comment"
          >
            Stream
          </label>
          <Select bind:value={selectedStreamId}>
            <SelectTrigger id="stream-select" class="w-full">
              <span class={selectedStreamId ? "" : "text-tokyo-night-comment"}>
                {selectedStreamLabel}
              </span>
            </SelectTrigger>
            <SelectContent>
              {#if streams.length === 0}
                <SelectItem value="" disabled>No streams available</SelectItem>
              {:else}
                {#each streams as stream (stream.id ?? stream.name)}
                  <SelectItem value={stream.id}>{stream.name}</SelectItem>
                {/each}
              {/if}
            </SelectContent>
          </Select>
        </div>

        <div class="space-y-2">
          <label
            for="clip-title"
            class="text-sm font-medium text-tokyo-night-comment"
          >
            Title
          </label>
          <Input
            id="clip-title"
            type="text"
            bind:value={clipTitle}
            placeholder="Enter clip title"
            required
          />
        </div>

        <div class="space-y-2">
          <label
            for="clip-description"
            class="text-sm font-medium text-tokyo-night-comment"
          >
            Description (optional)
          </label>
          <Textarea
            id="clip-description"
            bind:value={clipDescription}
            placeholder="Enter clip description"
            rows={3}
          />
        </div>

        <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <div class="space-y-2">
            <label
              for="start-time"
              class="text-sm font-medium text-tokyo-night-comment"
            >
              Start Time (seconds)
            </label>
            <Input
              id="start-time"
              type="number"
              bind:value={startTime}
              min="0"
              required
            />
          </div>

          <div class="space-y-2">
            <label
              for="end-time"
              class="text-sm font-medium text-tokyo-night-comment"
            >
              End Time (seconds)
            </label>
            <Input
              id="end-time"
              type="number"
              bind:value={endTime}
              min="1"
              required
            />
          </div>
        </div>

        <p class="text-sm text-tokyo-night-comment">
          Duration: {formatDuration(Math.max(0, endTime - startTime))}
        </p>
      </div>

      <DialogFooter class="gap-2">
        <Button
          type="button"
          variant="outline"
          onclick={() => (showCreateModal = false)}
          disabled={creatingClip}
        >
          Cancel
        </Button>
        <Button type="submit" disabled={creatingClip || !selectedStreamId}>
          {creatingClip ? "Creating..." : "Create Clip"}
        </Button>
      </DialogFooter>
    </form>
  </DialogContent>
</Dialog>

<!-- Clip Player Modal -->
<ClipModal clip={selectedClip} onClose={closeClip} />

<style>
  .line-clamp-2 {
    display: -webkit-box;
    -webkit-line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }
</style>
