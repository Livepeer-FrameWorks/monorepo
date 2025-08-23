<script>
  import { onMount, onDestroy } from "svelte";
  import { page } from "$app/stores";
  import { goto } from "$app/navigation";
  import { base } from "$app/paths";
  import { streamsService } from "$lib/graphql/services/streams.js";
  import { analyticsService } from "$lib/graphql/services/analytics.js";
  import LoadingCard from "$lib/components/LoadingCard.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import { getIconComponent } from "$lib/iconUtils.js";

  let streamId = $page.params.id;
  let stream = null;
  let streamKeys = [];
  let recordings = [];
  let analytics = null;
  let loading = true;
  let error = null;
  let activeTab = 'overview';
  let showEditModal = false;
  let showDeleteModal = false;
  let showCreateKeyModal = false;
  let editForm = {
    name: '',
    description: '',
    record: false
  };
  let createKeyForm = {
    keyName: '',
    isActive: true
  };
  let actionLoading = {
    refreshKey: false,
    deleteStream: false,
    editStream: false,
    createKey: false,
    deleteKey: null
  };

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
        loadAnalytics().catch(() => null) // Optional - don't fail if analytics unavailable
      ]);

      streamKeys = keysData || [];
      recordings = recordingsData || [];
      analytics = analyticsData;

      // Initialize edit form
      editForm = {
        name: stream.name || '',
        description: stream.description || '',
        record: stream.record || false
      };

    } catch (err) {
      console.error('Failed to load stream data:', err);
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
        loadAnalytics().catch(() => null)
      ]);
      
      if (updatedStream) {
        stream = updatedStream;
      }
      if (updatedAnalytics) {
        analytics = updatedAnalytics;
      }
    } catch (err) {
      console.error('Failed to refresh live data:', err);
    }
  }

  async function loadAnalytics() {
    const timeRange = {
      start: new Date(Date.now() - 24 * 60 * 60 * 1000).toISOString(),
      end: new Date().toISOString()
    };
    
    return await analyticsService.getEnhancedStreamAnalytics(streamId, timeRange);
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
      console.error('Failed to refresh stream key:', err);
      error = "Failed to refresh stream key";
    } finally {
      actionLoading.refreshKey = false;
    }
  }

  async function handleEditStream() {
    if (!stream) return;
    
    try {
      actionLoading.editStream = true;
      const updatedStream = await streamsService.updateStream(streamId, editForm);
      if (updatedStream) {
        stream = updatedStream;
        showEditModal = false;
      }
    } catch (err) {
      console.error('Failed to update stream:', err);
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
      goto(`${base}/streams`);
    } catch (err) {
      console.error('Failed to delete stream:', err);
      error = "Failed to delete stream";
      actionLoading.deleteStream = false;
    }
  }

  async function handleCreateStreamKey() {
    try {
      actionLoading.createKey = true;
      await streamsService.createStreamKey(streamId, createKeyForm);
      streamKeys = await streamsService.getStreamKeys(streamId);
      showCreateKeyModal = false;
      createKeyForm = { keyName: '', isActive: true };
    } catch (err) {
      console.error('Failed to create stream key:', err);
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
      console.error('Failed to delete stream key:', err);
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

  function getStatusColor(status) {
    switch (status?.toLowerCase()) {
      case 'live':
      case 'online':
      case 'active':
        return 'text-green-400';
      case 'offline':
      case 'inactive':
        return 'text-red-400';
      case 'recording':
        return 'text-yellow-400';
      default:
        return 'text-tokyo-night-comment';
    }
  }

  function getStatusIcon(status) {
    switch (status?.toLowerCase()) {
      case 'live':
      case 'online':
      case 'active':
        return 'Radio';
      case 'offline':
      case 'inactive':
        return 'RadioOff';
      case 'recording':
        return 'Video';
      default:
        return 'Circle';
    }
  }

  function formatDate(dateString) {
    return new Date(dateString).toLocaleString();
  }

  function formatBytes(bytes) {
    if (!bytes) return 'N/A';
    const sizes = ['Bytes', 'KB', 'MB', 'GB'];
    const i = Math.floor(Math.log(bytes) / Math.log(1024));
    return Math.round(bytes / Math.pow(1024, i) * 100) / 100 + ' ' + sizes[i];
  }

  function formatDuration(seconds) {
    if (!seconds) return 'N/A';
    const hours = Math.floor(seconds / 3600);
    const minutes = Math.floor((seconds % 3600) / 60);
    const secs = seconds % 60;
    return `${hours.toString().padStart(2, '0')}:${minutes.toString().padStart(2, '0')}:${secs.toString().padStart(2, '0')}`;
  }

  function navigateBack() {
    goto(`${base}/streams`);
  }

  function navigateToHealth() {
    goto(`${base}/streams/${streamId}/health`);
  }
</script>

<svelte:head>
  <title>Stream Details - {stream?.name || 'Loading...'} - FrameWorks</title>
</svelte:head>

<div class="min-h-screen bg-tokyo-night-bg text-tokyo-night-fg">
  <div class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
    <!-- Header -->
    <div class="mb-8">
      <div class="flex items-center justify-between mb-4">
        <div class="flex items-center space-x-4">
          <button
            on:click={navigateBack}
            class="p-2 rounded-lg bg-tokyo-night-surface hover:bg-tokyo-night-selection transition-colors"
          >
            <svelte:component this={getIconComponent('ArrowLeft')} class="w-5 h-5" />
          </button>
          
          <div>
            <h1 class="text-3xl font-bold text-tokyo-night-blue">
              {stream?.name || 'Stream Details'}
            </h1>
            <p class="text-tokyo-night-comment">
              Manage your stream settings, keys, and recordings
            </p>
          </div>
        </div>

        {#if stream && !loading}
          <div class="flex items-center space-x-3">
            <button
              on:click={navigateToHealth}
              class="btn-secondary"
            >
              <svelte:component this={getIconComponent('Activity')} class="w-4 h-4 mr-2" />
              Health Monitor
            </button>
            
            <button
              on:click={() => showEditModal = true}
              class="btn-secondary"
            >
              <svelte:component this={getIconComponent('Edit')} class="w-4 h-4 mr-2" />
              Edit
            </button>
            
            <button
              on:click={() => showDeleteModal = true}
              class="bg-red-600 hover:bg-red-700 text-white px-4 py-2 rounded-lg transition-colors"
            >
              <svelte:component this={getIconComponent('Trash2')} class="w-4 h-4 mr-2" />
              Delete
            </button>
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
      <div class="bg-red-900/20 border border-red-500/30 rounded-lg p-6 text-center">
        <svelte:component this={getIconComponent('AlertTriangle')} class="w-12 h-12 text-red-400 mx-auto mb-4" />
        <h3 class="text-lg font-semibold text-red-400 mb-2">Error Loading Stream</h3>
        <p class="text-red-300">{error}</p>
        <button
          on:click={loadStreamData}
          class="mt-4 px-4 py-2 bg-red-600 hover:bg-red-700 text-white rounded-lg transition-colors"
        >
          Retry
        </button>
      </div>
    {:else if stream}
      <!-- Stream Overview Cards -->
      <div class="grid grid-cols-1 md:grid-cols-3 gap-6 mb-8">
        <!-- Status Card -->
        <div class="glass-card p-6">
          <div class="flex items-center justify-between mb-4">
            <h3 class="text-lg font-semibold text-tokyo-night-cyan">Stream Status</h3>
            <svelte:component 
              this={getIconComponent(getStatusIcon(stream.status))} 
              class="w-6 h-6 {getStatusColor(stream.status)}" 
            />
          </div>
          <div class="space-y-2">
            <div class="flex justify-between">
              <span class="text-tokyo-night-comment">Status:</span>
              <span class="font-mono {getStatusColor(stream.status)} uppercase">
                {stream.status || 'Unknown'}
              </span>
            </div>
            <div class="flex justify-between">
              <span class="text-tokyo-night-comment">Recording:</span>
              <span class="font-mono {stream.record ? 'text-green-400' : 'text-red-400'}">
                {stream.record ? 'Enabled' : 'Disabled'}
              </span>
            </div>
            {#if analytics?.currentViewers !== undefined}
              <div class="flex justify-between">
                <span class="text-tokyo-night-comment">Viewers:</span>
                <span class="font-mono text-tokyo-night-cyan">{analytics.currentViewers}</span>
              </div>
            {/if}
          </div>
        </div>

        <!-- Stream Key Card -->
        <div class="glass-card p-6">
          <div class="flex items-center justify-between mb-4">
            <h3 class="text-lg font-semibold text-tokyo-night-cyan">Stream Key</h3>
            <button
              on:click={handleRefreshStreamKey}
              disabled={actionLoading.refreshKey}
              class="p-2 rounded-lg bg-tokyo-night-bg-dark hover:bg-tokyo-night-selection transition-colors disabled:opacity-50"
            >
              <svelte:component 
                this={getIconComponent('RefreshCw')} 
                class="w-4 h-4 {actionLoading.refreshKey ? 'animate-spin' : ''}" 
              />
            </button>
          </div>
          <div class="space-y-3">
            <div>
              <span class="text-sm text-tokyo-night-comment">Primary Key</span>
              <div class="flex items-center space-x-2 mt-1">
                <code class="flex-1 px-3 py-2 bg-tokyo-night-bg-dark rounded-lg text-sm font-mono text-tokyo-night-cyan border border-tokyo-night-fg-gutter">
                  {stream.streamKey || 'Not set'}
                </code>
                <button
                  on:click={() => copyToClipboard(stream.streamKey)}
                  class="p-2 rounded-lg bg-tokyo-night-bg-dark hover:bg-tokyo-night-selection transition-colors"
                >
                  <svelte:component this={getIconComponent('Copy')} class="w-4 h-4" />
                </button>
              </div>
            </div>
          </div>
        </div>

        <!-- Playback Info Card -->
        <div class="glass-card p-6">
          <h3 class="text-lg font-semibold text-tokyo-night-cyan mb-4">Playback Info</h3>
          <div class="space-y-3">
            <div>
              <span class="text-sm text-tokyo-night-comment">Playback ID</span>
              <div class="flex items-center space-x-2 mt-1">
                <code class="flex-1 px-3 py-2 bg-tokyo-night-bg-dark rounded-lg text-sm font-mono text-tokyo-night-cyan border border-tokyo-night-fg-gutter">
                  {stream.playbackId || 'Not set'}
                </code>
                <button
                  on:click={() => copyToClipboard(stream.playbackId)}
                  class="p-2 rounded-lg bg-tokyo-night-bg-dark hover:bg-tokyo-night-selection transition-colors"
                >
                  <svelte:component this={getIconComponent('Copy')} class="w-4 h-4" />
                </button>
              </div>
            </div>
            <div class="text-sm text-tokyo-night-comment">
              Created: {formatDate(stream.createdAt)}
            </div>
          </div>
        </div>
      </div>

      <!-- Tabbed Content -->
      <div class="bg-tokyo-night-surface rounded-lg">
        <!-- Tab Navigation -->
        <div class="border-b border-tokyo-night-fg-gutter">
          <nav class="flex space-x-8 px-6">
            <button
              on:click={() => activeTab = 'overview'}
              class="tab-button {activeTab === 'overview' ? 'tab-active' : ''}"
            >
              <svelte:component this={getIconComponent('Info')} class="w-4 h-4 mr-2" />
              Overview
            </button>
            <button
              on:click={() => activeTab = 'keys'}
              class="tab-button {activeTab === 'keys' ? 'tab-active' : ''}"
            >
              <svelte:component this={getIconComponent('Key')} class="w-4 h-4 mr-2" />
              Stream Keys ({streamKeys.length})
            </button>
            <button
              on:click={() => activeTab = 'recordings'}
              class="tab-button {activeTab === 'recordings' ? 'tab-active' : ''}"
            >
              <svelte:component this={getIconComponent('Video')} class="w-4 h-4 mr-2" />
              Recordings ({recordings.length})
            </button>
          </nav>
        </div>

        <!-- Tab Content -->
        <div class="p-6">
          {#if activeTab === 'overview'}
            <!-- Overview Tab -->
            <div class="space-y-6">
              <div class="grid grid-cols-1 lg:grid-cols-2 gap-6">
                <!-- Stream Information -->
                <div>
                  <h4 class="text-lg font-semibold text-tokyo-night-cyan mb-4">Stream Information</h4>
                  <div class="space-y-3">
                    <div>
                      <span class="text-sm text-tokyo-night-comment">Name</span>
                      <p class="text-tokyo-night-fg font-medium">{stream.name}</p>
                    </div>
                    {#if stream.description}
                      <div>
                        <span class="text-sm text-tokyo-night-comment">Description</span>
                        <p class="text-tokyo-night-fg">{stream.description}</p>
                      </div>
                    {/if}
                    <div>
                      <span class="text-sm text-tokyo-night-comment">Created</span>
                      <p class="text-tokyo-night-fg">{formatDate(stream.createdAt)}</p>
                    </div>
                    <div>
                      <span class="text-sm text-tokyo-night-comment">Last Updated</span>
                      <p class="text-tokyo-night-fg">{formatDate(stream.updatedAt)}</p>
                    </div>
                  </div>
                </div>

                <!-- Quick Stats -->
                <div>
                  <h4 class="text-lg font-semibold text-tokyo-night-cyan mb-4">Quick Stats</h4>
                  <div class="space-y-3">
                    <div class="flex justify-between">
                      <span class="text-tokyo-night-comment">Total Stream Keys:</span>
                      <span class="font-mono text-tokyo-night-cyan">{streamKeys.length}</span>
                    </div>
                    <div class="flex justify-between">
                      <span class="text-tokyo-night-comment">Total Recordings:</span>
                      <span class="font-mono text-tokyo-night-cyan">{recordings.length}</span>
                    </div>
                    {#if analytics}
                      <div class="flex justify-between">
                        <span class="text-tokyo-night-comment">24h Peak Viewers:</span>
                        <span class="font-mono text-tokyo-night-cyan">{analytics.peakViewers || 0}</span>
                      </div>
                      <div class="flex justify-between">
                        <span class="text-tokyo-night-comment">Total Watch Time:</span>
                        <span class="font-mono text-tokyo-night-cyan">{formatDuration(analytics.totalWatchTime || 0)}</span>
                      </div>
                    {/if}
                  </div>
                </div>
              </div>
            </div>

          {:else if activeTab === 'keys'}
            <!-- Stream Keys Tab -->
            <div>
              <div class="flex items-center justify-between mb-6">
                <h4 class="text-lg font-semibold text-tokyo-night-cyan">Stream Keys Management</h4>
                <button
                  on:click={() => showCreateKeyModal = true}
                  class="btn-primary"
                >
                  <svelte:component this={getIconComponent('Plus')} class="w-4 h-4 mr-2" />
                  Create Key
                </button>
              </div>

              {#if streamKeys.length > 0}
                <div class="space-y-4">
                  {#each streamKeys as key}
                    <div class="border border-tokyo-night-fg-gutter rounded-lg p-4">
                      <div class="flex items-center justify-between">
                        <div class="flex-1">
                          <div class="flex items-center space-x-3 mb-2">
                            <h5 class="font-medium text-tokyo-night-fg">{key.keyName}</h5>
                            <span class="badge {key.isActive ? 'badge-success' : 'badge-danger'}">
                              {key.isActive ? 'Active' : 'Inactive'}
                            </span>
                          </div>
                          
                          <div class="flex items-center space-x-2 mb-2">
                            <code class="flex-1 px-3 py-2 bg-tokyo-night-bg-dark rounded-lg text-sm font-mono text-tokyo-night-cyan">
                              {key.keyValue}
                            </code>
                            <button
                              on:click={() => copyToClipboard(key.keyValue)}
                              class="p-2 rounded-lg bg-tokyo-night-bg-dark hover:bg-tokyo-night-selection transition-colors"
                            >
                              <svelte:component this={getIconComponent('Copy')} class="w-4 h-4" />
                            </button>
                          </div>
                          
                          <div class="text-sm text-tokyo-night-comment">
                            Created: {formatDate(key.createdAt)}
                            {#if key.lastUsedAt}
                              • Last used: {formatDate(key.lastUsedAt)}
                            {/if}
                          </div>
                        </div>
                        
                        <button
                          on:click={() => handleDeleteStreamKey(key.id)}
                          disabled={actionLoading.deleteKey === key.id}
                          class="ml-4 p-2 rounded-lg bg-red-600 hover:bg-red-700 text-white transition-colors disabled:opacity-50"
                        >
                          {#if actionLoading.deleteKey === key.id}
                            <svelte:component this={getIconComponent('Loader')} class="w-4 h-4 animate-spin" />
                          {:else}
                            <svelte:component this={getIconComponent('Trash2')} class="w-4 h-4" />
                          {/if}
                        </button>
                      </div>
                    </div>
                  {/each}
                </div>
              {:else}
                <EmptyState
                  icon="Key"
                  title="No Stream Keys"
                  description="Create your first stream key to start broadcasting"
                  actionText="Create Stream Key"
                  onAction={() => showCreateKeyModal = true}
                />
              {/if}
            </div>

          {:else if activeTab === 'recordings'}
            <!-- Recordings Tab -->
            <div>
              <h4 class="text-lg font-semibold text-tokyo-night-cyan mb-6">Stream Recordings</h4>

              {#if recordings.length > 0}
                <div class="grid grid-cols-1 lg:grid-cols-2 xl:grid-cols-3 gap-6">
                  {#each recordings as recording}
                    <div class="border border-tokyo-night-fg-gutter rounded-lg p-4">
                      {#if recording.thumbnailUrl}
                        <div class="aspect-video bg-tokyo-night-bg-dark rounded-lg mb-4 overflow-hidden">
                          <img 
                            src={recording.thumbnailUrl} 
                            alt={recording.title}
                            class="w-full h-full object-cover"
                          />
                        </div>
                      {:else}
                        <div class="aspect-video bg-tokyo-night-bg-dark rounded-lg mb-4 flex items-center justify-center">
                          <svelte:component this={getIconComponent('Video')} class="w-12 h-12 text-tokyo-night-comment" />
                        </div>
                      {/if}
                      
                      <h5 class="font-medium text-tokyo-night-fg mb-2">{recording.title}</h5>
                      
                      <div class="space-y-1 text-sm text-tokyo-night-comment mb-4">
                        <div class="flex justify-between">
                          <span>Duration:</span>
                          <span>{formatDuration(recording.duration)}</span>
                        </div>
                        <div class="flex justify-between">
                          <span>Size:</span>
                          <span>{formatBytes(recording.fileSizeBytes)}</span>
                        </div>
                        <div class="flex justify-between">
                          <span>Status:</span>
                          <span class={recording.status === 'ready' ? 'text-green-400' : 'text-yellow-400'}>
                            {recording.status}
                          </span>
                        </div>
                        <div class="flex justify-between">
                          <span>Visibility:</span>
                          <span>{recording.isPublic ? 'Public' : 'Private'}</span>
                        </div>
                      </div>
                      
                      <div class="text-xs text-tokyo-night-comment">
                        Recorded: {formatDate(recording.createdAt)}
                      </div>
                    </div>
                  {/each}
                </div>
              {:else}
                <EmptyState
                  icon="Video"
                  title="No Recordings"
                  description="No recordings found for this stream. Enable recording to start capturing your streams."
                />
              {/if}
            </div>
          {/if}
        </div>
      </div>
    {/if}
  </div>
</div>

<!-- Edit Stream Modal -->
{#if showEditModal}
  <div class="fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center z-50">
    <div class="bg-tokyo-night-surface rounded-lg p-6 w-full max-w-md mx-4">
      <h3 class="text-lg font-semibold text-tokyo-night-cyan mb-4">Edit Stream</h3>
      
      <form on:submit|preventDefault={handleEditStream}>
        <div class="space-y-4">
          <div>
            <label for="editName" class="block text-sm font-medium text-tokyo-night-fg mb-2">
              Stream Name
            </label>
            <input
              id="editName"
              type="text"
              bind:value={editForm.name}
              class="input"
              required
            />
          </div>
          
          <div>
            <label for="editDescription" class="block text-sm font-medium text-tokyo-night-fg mb-2">
              Description
            </label>
            <textarea
              id="editDescription"
              bind:value={editForm.description}
              class="input"
              rows="3"
            ></textarea>
          </div>
          
          <div class="flex items-center">
            <input
              id="editRecord"
              type="checkbox"
              bind:checked={editForm.record}
              class="rounded border-tokyo-night-fg-gutter text-tokyo-night-blue focus:ring-tokyo-night-blue"
            />
            <label for="editRecord" class="ml-2 text-sm text-tokyo-night-fg">
              Enable Recording
            </label>
          </div>
        </div>
        
        <div class="flex justify-end space-x-3 mt-6">
          <button
            type="button"
            on:click={() => showEditModal = false}
            class="btn-secondary"
          >
            Cancel
          </button>
          <button
            type="submit"
            disabled={actionLoading.editStream}
            class="btn-primary"
          >
            {#if actionLoading.editStream}
              <svelte:component this={getIconComponent('Loader')} class="w-4 h-4 mr-2 animate-spin" />
            {/if}
            Save Changes
          </button>
        </div>
      </form>
    </div>
  </div>
{/if}

<!-- Delete Stream Modal -->
{#if showDeleteModal}
  <div class="fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center z-50">
    <div class="bg-tokyo-night-surface rounded-lg p-6 w-full max-w-md mx-4">
      <h3 class="text-lg font-semibold text-red-400 mb-4">Delete Stream</h3>
      <p class="text-tokyo-night-fg mb-6">
        Are you sure you want to delete "<strong>{stream?.name}</strong>"? This action cannot be undone.
        All associated stream keys and recordings will also be deleted.
      </p>
      
      <div class="flex justify-end space-x-3">
        <button
          on:click={() => showDeleteModal = false}
          class="btn-secondary"
        >
          Cancel
        </button>
        <button
          on:click={handleDeleteStream}
          disabled={actionLoading.deleteStream}
          class="bg-red-600 hover:bg-red-700 text-white px-4 py-2 rounded-lg transition-colors disabled:opacity-50"
        >
          {#if actionLoading.deleteStream}
            <svelte:component this={getIconComponent('Loader')} class="w-4 h-4 mr-2 animate-spin" />
          {/if}
          Delete Stream
        </button>
      </div>
    </div>
  </div>
{/if}

<!-- Create Stream Key Modal -->
{#if showCreateKeyModal}
  <div class="fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center z-50">
    <div class="bg-tokyo-night-surface rounded-lg p-6 w-full max-w-md mx-4">
      <h3 class="text-lg font-semibold text-tokyo-night-cyan mb-4">Create Stream Key</h3>
      
      <form on:submit|preventDefault={handleCreateStreamKey}>
        <div class="space-y-4">
          <div>
            <label for="keyName" class="block text-sm font-medium text-tokyo-night-fg mb-2">
              Key Name
            </label>
            <input
              id="keyName"
              type="text"
              bind:value={createKeyForm.keyName}
              class="input"
              placeholder="e.g., OBS Studio, Mobile App"
              required
            />
          </div>
          
          <div class="flex items-center">
            <input
              id="keyActive"
              type="checkbox"
              bind:checked={createKeyForm.isActive}
              class="rounded border-tokyo-night-fg-gutter text-tokyo-night-blue focus:ring-tokyo-night-blue"
            />
            <label for="keyActive" class="ml-2 text-sm text-tokyo-night-fg">
              Active
            </label>
          </div>
        </div>
        
        <div class="flex justify-end space-x-3 mt-6">
          <button
            type="button"
            on:click={() => showCreateKeyModal = false}
            class="btn-secondary"
          >
            Cancel
          </button>
          <button
            type="submit"
            disabled={actionLoading.createKey}
            class="btn-primary"
          >
            {#if actionLoading.createKey}
              <svelte:component this={getIconComponent('Loader')} class="w-4 h-4 mr-2 animate-spin" />
            {/if}
            Create Key
          </button>
        </div>
      </form>
    </div>
  </div>
{/if}

<style>
  .tab-button {
    display: flex;
    align-items: center;
    padding: 0.75rem 0;
    color: #565f89;
    border-bottom: 2px solid transparent;
    transition: all 0.2s ease;
    font-weight: 500;
  }

  .tab-button:hover {
    color: #a9b1d6;
  }

  .tab-active {
    color: #7aa2f7;
    border-bottom-color: #7aa2f7;
  }
</style>