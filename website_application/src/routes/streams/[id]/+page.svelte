<script>
  import { onMount, onDestroy } from "svelte";
  import { page } from "$app/state";
  import { goto } from "$app/navigation";
  import { resolve } from "$app/paths";
  import { streamsService } from "$lib/graphql/services/streams.js";
  import { analyticsService } from "$lib/graphql/services/analytics.js";
  import LoadingCard from "$lib/components/LoadingCard.svelte";
  import { getIconComponent } from "$lib/iconUtils";
  import { Button } from "$lib/components/ui/button";
  import {
    Tabs,
    TabsContent,
    TabsList,
    TabsTrigger,
  } from "$lib/components/ui/tabs";
  import {
    StreamEditModal,
    StreamDeleteModal,
    StreamCreateKeyModal,
    StreamStatusCard,
    StreamKeyCard,
    StreamPlaybackCard,
    OverviewTabPanel,
    StreamKeysTabPanel,
    RecordingsTabPanel,
  } from "$lib/components/stream-details";

  let streamId = page.params.id;
  let stream = $state(null);
  let streamKeys = $state([]);
  let recordings = $state([]);
  let analytics = $state(null);
  let loading = $state(true);
  let error = $state(null);
  let showEditModal = $state(false);
  let showDeleteModal = $state(false);
  let showCreateKeyModal = $state(false);
  let actionLoading = $state({
    refreshKey: false,
    deleteStream: false,
    editStream: false,
    createKey: false,
    deleteKey: null,
  });

  // Auto-refresh interval for live data
  let refreshInterval = null;

  onMount(async () => {
    await loadStreamData();

    // Set up auto-refresh every 30 seconds for live data
    refreshInterval = setInterval(loadLiveData, 30000);
  });

  onDestroy(() => {
    if (refreshInterval) {
      clearInterval(refreshInterval);
    }
  });

  async function loadStreamData() {
    try {
      loading = true;
      error = null;

      // Load stream details first
      stream = await streamsService.getStream(streamId);

      if (!stream) {
        error = "Stream not found";
        loading = false;
        return;
      }

      // Load additional data in parallel
      const [keysData, recordingsData, analyticsData] = await Promise.all([
        streamsService.getStreamKeys(streamId),
        streamsService.getStreamRecordings(streamId),
        loadAnalytics().catch(() => null), // Optional - don't fail if analytics unavailable
      ]);

      streamKeys = keysData || [];
      recordings = recordingsData || [];
      analytics = analyticsData;
    } catch (err) {
      console.error("Failed to load stream data:", err);
      error = "Failed to load stream data";
    } finally {
      loading = false;
    }
  }

  async function loadLiveData() {
    try {
      // Refresh stream status and analytics without showing loading
      const [updatedStream, updatedAnalytics] = await Promise.all([
        streamsService.getStream(streamId),
        loadAnalytics().catch(() => null),
      ]);

      if (updatedStream) {
        stream = updatedStream;
      }
      if (updatedAnalytics) {
        analytics = updatedAnalytics;
      }
    } catch (err) {
      console.error("Failed to refresh live data:", err);
    }
  }

  async function loadAnalytics() {
    const timeRange = {
      start: new Date(Date.now() - 24 * 60 * 60 * 1000).toISOString(),
      end: new Date().toISOString(),
    };

    return await analyticsService.getEnhancedStreamAnalytics(
      streamId,
      timeRange,
    );
  }

  async function handleRefreshStreamKey() {
    if (!stream) return;

    try {
      actionLoading.refreshKey = true;
      const updatedStream = await streamsService.refreshStreamKey(streamId);
      if (updatedStream) {
        stream = updatedStream;
      }
    } catch (err) {
      console.error("Failed to refresh stream key:", err);
      error = "Failed to refresh stream key";
    } finally {
      actionLoading.refreshKey = false;
    }
  }

  async function handleEditStream(formData) {
    if (!stream) return;

    try {
      actionLoading.editStream = true;
      const updatedStream = await streamsService.updateStream(
        streamId,
        formData,
      );
      if (updatedStream) {
        stream = updatedStream;
        showEditModal = false;
      }
    } catch (err) {
      console.error("Failed to update stream:", err);
      error = "Failed to update stream";
    } finally {
      actionLoading.editStream = false;
    }
  }

  async function handleDeleteStream() {
    if (!stream) return;

    try {
      actionLoading.deleteStream = true;
      await streamsService.deleteStream(streamId);
      goto(resolve("/streams"));
    } catch (err) {
      console.error("Failed to delete stream:", err);
      error = "Failed to delete stream";
      actionLoading.deleteStream = false;
    }
  }

  async function handleCreateStreamKey(formData) {
    try {
      actionLoading.createKey = true;
      await streamsService.createStreamKey(streamId, formData);
      streamKeys = await streamsService.getStreamKeys(streamId);
      showCreateKeyModal = false;
    } catch (err) {
      console.error("Failed to create stream key:", err);
      error = "Failed to create stream key";
    } finally {
      actionLoading.createKey = false;
    }
  }

  async function handleDeleteStreamKey(keyId) {
    try {
      actionLoading.deleteKey = keyId;
      await streamsService.deleteStreamKey(streamId, keyId);
      streamKeys = await streamsService.getStreamKeys(streamId);
    } catch (err) {
      console.error("Failed to delete stream key:", err);
      error = "Failed to delete stream key";
    } finally {
      actionLoading.deleteKey = null;
    }
  }

  function copyToClipboard(text) {
    navigator.clipboard.writeText(text).then(() => {
      // Could add toast notification here
    });
  }

  function navigateBack() {
    goto(resolve("/streams"));
  }

  function navigateToHealth() {
    goto(resolve(`/streams/${streamId}/health`));
  }

  const SvelteComponent = $derived(getIconComponent("ArrowLeft"));
</script>

<svelte:head>
  <title>Stream Details - {stream?.name || "Loading..."} - FrameWorks</title>
</svelte:head>

<div class="min-h-screen bg-tokyo-night-bg text-tokyo-night-fg">
  <div class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
    <!-- Header -->
    <div class="mb-8">
      <div class="flex items-center justify-between mb-4">
        <div class="flex items-center space-x-4">
          <button
            onclick={navigateBack}
            class="p-2 rounded-lg bg-tokyo-night-surface hover:bg-tokyo-night-selection transition-colors"
          >
            <SvelteComponent class="w-5 h-5" />
          </button>

          <div>
            <h1 class="text-3xl font-bold text-tokyo-night-blue">
              {stream?.name || "Stream Details"}
            </h1>
            <p class="text-tokyo-night-comment">
              Manage your stream settings, keys, and recordings
            </p>
          </div>
        </div>

        {#if stream && !loading}
          {@const SvelteComponent_1 = getIconComponent("Activity")}
          {@const SvelteComponent_2 = getIconComponent("Edit")}
          {@const SvelteComponent_3 = getIconComponent("Trash2")}
          <div class="flex items-center space-x-3">
            <Button variant="outline" class="gap-2" onclick={navigateToHealth}>
              <SvelteComponent_1 class="w-4 h-4" />
              Health Monitor
            </Button>

            <Button
              variant="outline"
              class="gap-2"
              onclick={() => (showEditModal = true)}
            >
              <SvelteComponent_2 class="w-4 h-4" />
              Edit
            </Button>

            <Button
              variant="destructive"
              class="gap-2"
              onclick={() => (showDeleteModal = true)}
            >
              <SvelteComponent_3 class="w-4 h-4" />
              Delete
            </Button>
          </div>
        {/if}
      </div>
    </div>

    {#if loading}
      <div class="space-y-6">
        <LoadingCard variant="analytics" />
        <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
          <LoadingCard variant="analytics" />
          <LoadingCard variant="analytics" />
        </div>
      </div>
    {:else if error}
      {@const SvelteComponent_4 = getIconComponent("AlertTriangle")}
      <div
        class="bg-red-900/20 border border-red-500/30 rounded-lg p-6 text-center"
      >
        <SvelteComponent_4 class="w-12 h-12 text-red-400 mx-auto mb-4" />
        <h3 class="text-lg font-semibold text-red-400 mb-2">
          Error Loading Stream
        </h3>
        <p class="text-red-300">{error}</p>
        <button
          onclick={loadStreamData}
          class="mt-4 px-4 py-2 bg-red-600 hover:bg-red-700 text-white rounded-lg transition-colors"
        >
          Retry
        </button>
      </div>
    {:else if stream}
      <!-- Stream Overview Cards -->
      <div class="grid grid-cols-1 md:grid-cols-3 gap-6 mb-8">
        <StreamStatusCard {stream} {analytics} />
        <StreamKeyCard
          {stream}
          loading={actionLoading.refreshKey}
          onRefresh={handleRefreshStreamKey}
          onCopy={copyToClipboard}
        />
        <StreamPlaybackCard {stream} onCopy={copyToClipboard} />
      </div>

      <!-- Tabbed Content -->
      <div class="bg-tokyo-night-surface rounded-lg">
        <Tabs defaultValue="overview" class="w-full">
          <TabsList
            class="flex space-x-8 px-6 border-b border-tokyo-night-fg-gutter bg-transparent"
          >
            {@const InfoIcon = getIconComponent("Info")}
            {@const KeyIcon = getIconComponent("Key")}
            {@const VideoIcon = getIconComponent("Video")}
            <TabsTrigger
              value="overview"
              class="gap-2 px-0 py-4 text-sm font-medium text-tokyo-night-comment border-b-2 border-transparent rounded-none data-[state=active]:text-tokyo-night-cyan data-[state=active]:border-tokyo-night-cyan"
            >
              <InfoIcon class="w-4 h-4" />
              Overview
            </TabsTrigger>
            <TabsTrigger
              value="keys"
              class="gap-2 px-0 py-4 text-sm font-medium text-tokyo-night-comment border-b-2 border-transparent rounded-none data-[state=active]:text-tokyo-night-cyan data-[state=active]:border-tokyo-night-cyan"
            >
              <KeyIcon class="w-4 h-4" />
              Stream Keys ({streamKeys.length})
            </TabsTrigger>
            <TabsTrigger
              value="recordings"
              class="gap-2 px-0 py-4 text-sm font-medium text-tokyo-night-comment border-b-2 border-transparent rounded-none data-[state=active]:text-tokyo-night-cyan data-[state=active]:border-tokyo-night-cyan"
            >
              <VideoIcon class="w-4 h-4" />
              Recordings ({recordings.length})
            </TabsTrigger>
          </TabsList>

          <TabsContent value="overview" class="p-6 space-y-6">
            <OverviewTabPanel
              {stream}
              {streamKeys}
              {recordings}
              {analytics}
            />
          </TabsContent>

          <TabsContent value="keys" class="p-6 space-y-6">
            <StreamKeysTabPanel
              {streamKeys}
              onCreateKey={() => (showCreateKeyModal = true)}
              onCopyKey={copyToClipboard}
              onDeleteKey={handleDeleteStreamKey}
              deleteLoading={actionLoading.deleteKey}
            />
          </TabsContent>

          <TabsContent value="recordings" class="p-6 space-y-6">
            <RecordingsTabPanel
              {recordings}
              onEnableRecording={() => (showEditModal = true)}
              onCopyLink={copyToClipboard}
              resolveUrl={resolve}
            />
          </TabsContent>
        </Tabs>
      </div>
    {/if}
    <StreamEditModal
      bind:open={showEditModal}
      {stream}
      loading={actionLoading.editStream}
      onSave={handleEditStream}
    />
    <StreamDeleteModal
      bind:open={showDeleteModal}
      streamName={stream?.name || ""}
      loading={actionLoading.deleteStream}
      onConfirm={handleDeleteStream}
    />
    <StreamCreateKeyModal
      bind:open={showCreateKeyModal}
      loading={actionLoading.createKey}
      onCreate={handleCreateStreamKey}
    />
  </div>
</div>
