<script>
  import { onMount } from "svelte";
  import { auth } from "$lib/stores/auth";
  import { streamsService } from "$lib/graphql/services/streams.js";
  import { clipsService } from "$lib/graphql/services/clips.js";
  import { toast } from "$lib/stores/toast.js";
  import LoadingCard from "$lib/components/LoadingCard.svelte";
  import LoadingSpinner from "$lib/components/LoadingSpinner.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import ClipModal from "$lib/components/ClipModal.svelte";
  import { getIconComponent } from "$lib/iconUtils.js";

  let isAuthenticated = false;
  let user = null;
  let loading = true;
  
  // Data
  let streams = [];
  let clips = [];
  
  // Clip creation
  let showCreateModal = false;
  let creatingClip = false;
  let selectedStreamId = "";
  let clipTitle = "";
  let clipDescription = "";
  let startTime = 0;
  let endTime = 300; // 5 minutes default

  // Clip viewing
  let selectedClip = null;

  // Subscribe to auth store
  auth.subscribe((authState) => {
    isAuthenticated = authState.isAuthenticated;
    user = authState.user;
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
      streams = await streamsService.getStreams();
      clips = await clipsService.getClips(); // Load all clips
    } catch (error) {
      console.error("Failed to load data:", error);
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
        endTime: Math.floor(endTime)
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
    return `${minutes}:${remainingSeconds.toString().padStart(2, '0')}`;
  }

  function formatDate(dateString) {
    return new Date(dateString).toLocaleDateString();
  }

  function getStreamName(streamId) {
    const stream = streams.find(s => s.id === streamId);
    return stream ? stream.name : streamId || 'Unknown Stream';
  }

  function openClip(clip) {
    // Add stream name to clip for modal display
    selectedClip = {
      ...clip,
      streamName: getStreamName(clip.streamId || clip.stream)
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
      <button
        on:click={() => showCreateModal = true}
        class="bg-tokyo-night-blue text-white px-4 py-2 rounded-lg hover:bg-blue-600 transition-colors"
        disabled={streams.length === 0}
      >
        Create Clip
      </button>
    </div>

    {#if loading}
      <!-- Loading skeleton for clips grid -->
      <div class="bg-tokyo-night-surface rounded-lg p-6">
        <div class="skeleton-text-lg w-24 mb-4"></div>
        <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
          {#each Array(6) as _}
            <LoadingCard variant="clip" />
          {/each}
        </div>
      </div>
    {:else}
      <!-- Clips Grid -->
      <div class="bg-tokyo-night-surface rounded-lg p-6">
        <h2 class="text-xl font-semibold mb-4 text-tokyo-night-cyan">Your Clips</h2>
        
        {#if clips.length === 0}
          <EmptyState 
            title="No clips yet"
            description="Create your first clip from a stream to get started"
            actionText={streams.length > 0 ? "Create Your First Clip" : ""}
            onAction={() => showCreateModal = true}
            showAction={streams.length > 0}
          >
            <svelte:component this={getIconComponent('Scissors')} class="w-12 h-12 text-tokyo-night-fg-dark mx-auto mb-4" />
            {#if streams.length === 0}
              <p class="text-tokyo-night-comment text-sm mt-2">
                You need at least one stream to create clips
              </p>
            {/if}
          </EmptyState>
        {:else}
          <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
            {#each clips as clip}
              <div class="bg-tokyo-night-bg rounded-lg p-4 border border-tokyo-night-selection">
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
                    <span class="px-2 py-1 text-xs rounded-full bg-tokyo-night-blue bg-opacity-20 text-tokyo-night-blue">
                      {clip.status}
                    </span>
                  </div>
                  <div class="flex justify-between">
                    <span class="text-tokyo-night-comment">Created</span>
                    <span>{formatDate(clip.createdAt)}</span>
                  </div>
                </div>
                
                <div class="mt-4 pt-4 border-t border-tokyo-night-selection">
                  <button
                    type="button"
                    on:click={() => openClip(clip)}
                    class="flex items-center space-x-2 text-tokyo-night-cyan hover:text-tokyo-night-blue text-sm font-medium bg-transparent border-none cursor-pointer transition-colors"
                  >
                    <svelte:component this={getIconComponent('Play')} class="w-4 h-4" />
                    <span>Play Clip</span>
                  </button>
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
{#if showCreateModal}
  <div class="fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center p-4 z-50">
    <div class="bg-tokyo-night-surface rounded-lg p-6 w-full max-w-md">
      <h2 class="text-xl font-semibold mb-4 text-tokyo-night-cyan">Create New Clip</h2>
      
      <form on:submit|preventDefault={createClip} class="space-y-4">
        <div>
          <label for="stream-select" class="block text-sm font-medium mb-2">Stream</label>
          <select
            id="stream-select"
            bind:value={selectedStreamId}
            class="w-full px-3 py-2 bg-tokyo-night-bg border border-tokyo-night-selection rounded-lg focus:outline-none focus:border-tokyo-night-blue"
            required
          >
            <option value="">Select a stream</option>
            {#each streams as stream}
              <option value={stream.id}>{stream.name}</option>
            {/each}
          </select>
        </div>
        
        <div>
          <label for="clip-title" class="block text-sm font-medium mb-2">Title</label>
          <input
            id="clip-title"
            type="text"
            bind:value={clipTitle}
            placeholder="Enter clip title"
            class="w-full px-3 py-2 bg-tokyo-night-bg border border-tokyo-night-selection rounded-lg focus:outline-none focus:border-tokyo-night-blue"
            required
          />
        </div>
        
        <div>
          <label for="clip-description" class="block text-sm font-medium mb-2">Description (optional)</label>
          <textarea
            id="clip-description"
            bind:value={clipDescription}
            placeholder="Enter clip description"
            rows="3"
            class="w-full px-3 py-2 bg-tokyo-night-bg border border-tokyo-night-selection rounded-lg focus:outline-none focus:border-tokyo-night-blue resize-none"
          ></textarea>
        </div>
        
        <div class="grid grid-cols-2 gap-4">
          <div>
            <label for="start-time" class="block text-sm font-medium mb-2">Start Time (seconds)</label>
            <input
              id="start-time"
              type="number"
              bind:value={startTime}
              min="0"
              class="w-full px-3 py-2 bg-tokyo-night-bg border border-tokyo-night-selection rounded-lg focus:outline-none focus:border-tokyo-night-blue"
              required
            />
          </div>
          
          <div>
            <label for="end-time" class="block text-sm font-medium mb-2">End Time (seconds)</label>
            <input
              id="end-time"
              type="number"
              bind:value={endTime}
              min="1"
              class="w-full px-3 py-2 bg-tokyo-night-bg border border-tokyo-night-selection rounded-lg focus:outline-none focus:border-tokyo-night-blue"
              required
            />
          </div>
        </div>
        
        <div class="text-sm text-tokyo-night-comment">
          Duration: {formatDuration(Math.max(0, endTime - startTime))}
        </div>
        
        <div class="flex space-x-3 pt-4">
          <button
            type="submit"
            disabled={creatingClip}
            class="flex-1 bg-tokyo-night-blue text-white py-2 px-4 rounded-lg hover:bg-blue-600 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
          >
            {creatingClip ? "Creating..." : "Create Clip"}
          </button>
          
          <button
            type="button"
            on:click={() => showCreateModal = false}
            class="px-4 py-2 border border-tokyo-night-selection rounded-lg hover:bg-tokyo-night-selection transition-colors"
            disabled={creatingClip}
          >
            Cancel
          </button>
        </div>
      </form>
    </div>
  </div>
{/if}

<!-- Clip Player Modal -->
<ClipModal 
  clip={selectedClip}
  onClose={closeClip}
/>

<style>
  .line-clamp-2 {
    display: -webkit-box;
    -webkit-line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }
</style>