<script>
  import { onMount } from 'svelte';
  import { dvrService } from '$lib/graphql/services/dvr.js';
  import { formatBytes, formatDuration, formatDate } from '$lib/utils/formatters.js';

  let recordings = [];
  let loading = true;
  let error = null;
  let statusFilter = 'all';
  let searchQuery = '';

  // Pagination
  let currentPage = 1;
  let pageSize = 20;
  let totalRecordings = 0;

  $: filteredRecordings = recordings.filter(recording => {
    const matchesStatus = statusFilter === 'all' || recording.status === statusFilter;
    const matchesSearch = !searchQuery || 
      recording.internalName?.toLowerCase().includes(searchQuery.toLowerCase()) ||
      recording.dvrHash?.toLowerCase().includes(searchQuery.toLowerCase()) ||
      recording.manifestPath?.toLowerCase().includes(searchQuery.toLowerCase());
    
    return matchesStatus && matchesSearch;
  });

  $: paginatedRecordings = filteredRecordings.slice(
    (currentPage - 1) * pageSize,
    currentPage * pageSize
  );

  $: totalPages = Math.ceil(filteredRecordings.length / pageSize);
  $: totalRecordings = filteredRecordings.length;

  async function loadRecordings() {
    try {
      loading = true;
      const result = await dvrService.getDVRRequests();

      if (result.success) {
        recordings = result.recordings || [];
        error = null;
      } else {
        error = result.error || 'Failed to load recordings';
        recordings = [];
      }
    } catch (err) {
      console.error('Failed to load recordings:', err);
      error = 'Failed to load recordings';
      recordings = [];
    } finally {
      loading = false;
    }
  }

  function getStatusColor(status) {
    switch (status?.toLowerCase()) {
      case 'completed': return 'text-tokyo-night-green';
      case 'recording': return 'text-tokyo-night-yellow';
      case 'processing': return 'text-tokyo-night-blue';
      case 'failed': return 'text-tokyo-night-red';
      case 'paused': return 'text-tokyo-night-comment';
      default: return 'text-tokyo-night-fg-dark';
    }
  }

  function getStatusIcon(status) {
    switch (status?.toLowerCase()) {
      case 'completed': return '✓';
      case 'recording': return '●';
      case 'processing': return '⟳';
      case 'failed': return '✗';
      case 'paused': return '⏸';
      default: return '?';
    }
  }

  function nextPage() {
    if (currentPage < totalPages) currentPage++;
  }

  function prevPage() {
    if (currentPage > 1) currentPage--;
  }

  function goToPage(page) {
    if (page >= 1 && page <= totalPages) {
      currentPage = page;
    }
  }

  onMount(() => {
    loadRecordings();
  });
</script>

<svelte:head>
  <title>Recordings - FrameWorks</title>
</svelte:head>

<div class="min-h-screen bg-tokyo-night-bg text-tokyo-night-fg">
  <div class="container mx-auto px-6 py-8">
    <!-- Header -->
    <div class="mb-8">
      <h1 class="text-3xl font-bold text-tokyo-night-fg mb-2">Recordings</h1>
      <p class="text-tokyo-night-fg-dark">Manage and monitor all stream recordings</p>
    </div>

    <!-- Controls -->
    <div class="bg-tokyo-night-bg-light rounded-lg p-6 mb-6 border border-tokyo-night-fg-gutter">
      <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
        <!-- Search -->
        <div>
          <label for="search" class="block text-sm font-medium text-tokyo-night-fg-dark mb-2">
            Search Recordings
          </label>
          <input
            id="search"
            type="text"
            bind:value={searchQuery}
            placeholder="Search by stream name, hash, or path..."
            class="w-full px-3 py-2 bg-tokyo-night-bg-dark border border-tokyo-night-fg-gutter rounded-md text-tokyo-night-fg placeholder-tokyo-night-comment focus:outline-none focus:ring-2 focus:ring-tokyo-night-blue focus:border-transparent"
          />
        </div>

        <!-- Status Filter -->
        <div>
          <label for="status-filter" class="block text-sm font-medium text-tokyo-night-fg-dark mb-2">
            Status
          </label>
          <select
            id="status-filter"
            bind:value={statusFilter}
            class="w-full px-3 py-2 bg-tokyo-night-bg-dark border border-tokyo-night-fg-gutter rounded-md text-tokyo-night-fg focus:outline-none focus:ring-2 focus:ring-tokyo-night-blue focus:border-transparent"
          >
            <option value="all">All Statuses</option>
            <option value="recording">Recording</option>
            <option value="completed">Completed</option>
            <option value="processing">Processing</option>
            <option value="failed">Failed</option>
            <option value="paused">Paused</option>
          </select>
        </div>
      </div>

      <!-- Stats -->
      <div class="mt-6 pt-6 border-t border-tokyo-night-fg-gutter">
        <div class="grid grid-cols-2 md:grid-cols-4 gap-4 text-center">
          <div>
            <div class="text-2xl font-bold text-tokyo-night-fg">{totalRecordings}</div>
            <div class="text-sm text-tokyo-night-fg-dark">Total Results</div>
          </div>
          <div>
            <div class="text-2xl font-bold text-tokyo-night-green">
              {filteredRecordings.filter(r => r.status === 'completed').length}
            </div>
            <div class="text-sm text-tokyo-night-fg-dark">Completed</div>
          </div>
          <div>
            <div class="text-2xl font-bold text-tokyo-night-yellow">
              {filteredRecordings.filter(r => r.status === 'recording').length}
            </div>
            <div class="text-sm text-tokyo-night-fg-dark">Recording</div>
          </div>
          <div>
            <div class="text-2xl font-bold text-tokyo-night-red">
              {filteredRecordings.filter(r => r.status === 'failed').length}
            </div>
            <div class="text-sm text-tokyo-night-fg-dark">Failed</div>
          </div>
        </div>
      </div>
    </div>

    {#if loading}
      <div class="flex justify-center items-center py-12">
        <div class="animate-spin rounded-full h-8 w-8 border-b-2 border-tokyo-night-blue"></div>
        <span class="ml-3 text-tokyo-night-fg-dark">Loading recordings...</span>
      </div>
    {:else if error}
      <div class="bg-tokyo-night-red/10 border border-tokyo-night-red/30 rounded-lg p-6 text-center">
        <div class="text-tokyo-night-red mb-2">Error</div>
        <div class="text-tokyo-night-fg">{error}</div>
        <button 
          on:click={loadRecordings}
          class="mt-4 px-4 py-2 bg-tokyo-night-blue hover:bg-tokyo-night-blue/80 rounded-md text-tokyo-night-bg transition-colors"
        >
          Retry
        </button>
      </div>
    {:else if paginatedRecordings.length === 0}
      <div class="bg-tokyo-night-bg-light rounded-lg p-12 text-center border border-tokyo-night-fg-gutter">
        <div class="text-tokyo-night-fg-dark mb-4">No recordings found</div>
        {#if searchQuery || statusFilter !== 'all'}
          <div class="text-tokyo-night-comment text-sm mb-4">Try adjusting your filters</div>
          <button 
            on:click={() => { searchQuery = ''; statusFilter = 'all'; }}
            class="px-4 py-2 bg-tokyo-night-bg-dark hover:bg-tokyo-night-fg-gutter rounded-md text-tokyo-night-fg transition-colors"
          >
            Clear Filters
          </button>
        {/if}
      </div>
    {:else}
      <!-- Recordings Table -->
      <div class="bg-tokyo-night-bg-light rounded-lg border border-tokyo-night-fg-gutter overflow-hidden">
        <div class="overflow-x-auto">
          <table class="w-full">
            <thead class="bg-tokyo-night-bg-dark">
              <tr>
                <th class="px-6 py-3 text-left text-xs font-medium text-tokyo-night-fg-dark uppercase tracking-wider">
                  Recording
                </th>
                <th class="px-6 py-3 text-left text-xs font-medium text-tokyo-night-fg-dark uppercase tracking-wider">
                  Stream
                </th>
                <th class="px-6 py-3 text-left text-xs font-medium text-tokyo-night-fg-dark uppercase tracking-wider">
                  Status
                </th>
                <th class="px-6 py-3 text-left text-xs font-medium text-tokyo-night-fg-dark uppercase tracking-wider">
                  Duration
                </th>
                <th class="px-6 py-3 text-left text-xs font-medium text-tokyo-night-fg-dark uppercase tracking-wider">
                  Size
                </th>
                <th class="px-6 py-3 text-left text-xs font-medium text-tokyo-night-fg-dark uppercase tracking-wider">
                  Created
                </th>
                <th class="px-6 py-3 text-left text-xs font-medium text-tokyo-night-fg-dark uppercase tracking-wider">
                  Actions
                </th>
              </tr>
            </thead>
            <tbody class="divide-y divide-tokyo-night-fg-gutter">
              {#each paginatedRecordings as recording (recording.dvrHash)}
                <tr class="hover:bg-tokyo-night-bg-dark/50 transition-colors">
                  <td class="px-6 py-4">
                    <div class="flex flex-col">
                      <div class="text-sm font-medium text-tokyo-night-fg truncate max-w-xs" title={recording.manifestPath}>
                        {recording.manifestPath || recording.dvrHash}
                      </div>
                      <div class="text-xs text-tokyo-night-comment font-mono">
                        {recording.dvrHash}
                      </div>
                    </div>
                  </td>
                  <td class="px-6 py-4">
                    <div class="flex flex-col">
                      <div class="text-sm text-tokyo-night-fg">
                        {recording.internalName || 'Unknown'}
                      </div>
                      <div class="text-xs text-tokyo-night-comment">
                        {recording.storageNodeId || 'N/A'}
                      </div>
                    </div>
                  </td>
                  <td class="px-6 py-4">
                    <span class="inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium bg-tokyo-night-bg-dark {getStatusColor(recording.status)}">
                      <span class="mr-1">{getStatusIcon(recording.status)}</span>
                      {recording.status || 'Unknown'}
                    </span>
                  </td>
                  <td class="px-6 py-4 text-sm text-tokyo-night-fg">
                    {recording.durationSeconds ? formatDuration(recording.durationSeconds * 1000) : 'N/A'}
                  </td>
                  <td class="px-6 py-4 text-sm text-tokyo-night-fg">
                    {recording.sizeBytes ? formatBytes(recording.sizeBytes) : 'N/A'}
                  </td>
                  <td class="px-6 py-4 text-sm text-tokyo-night-fg">
                    {recording.createdAt ? formatDate(recording.createdAt) : 'N/A'}
                  </td>
                  <td class="px-6 py-4">
                    <div class="flex space-x-2">
                      {#if recording.status === 'completed' && recording.manifestPath}
                        <a
                          href={recording.manifestPath}
                          class="text-tokyo-night-cyan hover:text-tokyo-night-blue text-sm font-medium"
                          title="View recording manifest"
                        >
                          View
                        </a>
                      {/if}
                      {#if recording.status === 'recording'}
                        <button
                          class="text-tokyo-night-yellow hover:text-tokyo-night-orange text-sm font-medium"
                          title="Stop recording"
                        >
                          Stop
                        </button>
                      {/if}
                      <button
                        class="text-tokyo-night-fg-dark hover:text-tokyo-night-fg text-sm font-medium"
                        title="View details"
                      >
                        Details
                      </button>
                    </div>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>

        <!-- Pagination -->
        {#if totalPages > 1}
          <div class="bg-tokyo-night-bg-dark px-6 py-3 flex items-center justify-between border-t border-tokyo-night-fg-gutter">
            <div class="flex-1 flex justify-between sm:hidden">
              <button
                on:click={prevPage}
                disabled={currentPage === 1}
                class="relative inline-flex items-center px-4 py-2 border border-tokyo-night-fg-gutter text-sm font-medium rounded-md text-tokyo-night-fg bg-tokyo-night-bg-light hover:bg-tokyo-night-bg disabled:opacity-50 disabled:cursor-not-allowed"
              >
                Previous
              </button>
              <button
                on:click={nextPage}
                disabled={currentPage === totalPages}
                class="ml-3 relative inline-flex items-center px-4 py-2 border border-tokyo-night-fg-gutter text-sm font-medium rounded-md text-tokyo-night-fg bg-tokyo-night-bg-light hover:bg-tokyo-night-bg disabled:opacity-50 disabled:cursor-not-allowed"
              >
                Next
              </button>
            </div>
            <div class="hidden sm:flex-1 sm:flex sm:items-center sm:justify-between">
              <div>
                <p class="text-sm text-tokyo-night-fg-dark">
                  Showing
                  <span class="font-medium">{(currentPage - 1) * pageSize + 1}</span>
                  to
                  <span class="font-medium">{Math.min(currentPage * pageSize, totalRecordings)}</span>
                  of
                  <span class="font-medium">{totalRecordings}</span>
                  results
                </p>
              </div>
              <div>
                <nav class="relative z-0 inline-flex rounded-md shadow-sm -space-x-px">
                  <button
                    on:click={prevPage}
                    disabled={currentPage === 1}
                    class="relative inline-flex items-center px-2 py-2 rounded-l-md border border-tokyo-night-fg-gutter bg-tokyo-night-bg-light text-sm font-medium text-tokyo-night-fg hover:bg-tokyo-night-bg disabled:opacity-50 disabled:cursor-not-allowed"
                  >
                    ←
                  </button>
                  {#each Array.from({ length: Math.min(7, totalPages) }, (_, i) => {
                    if (totalPages <= 7) return i + 1;
                    if (currentPage <= 4) return i + 1;
                    if (currentPage >= totalPages - 3) return totalPages - 6 + i;
                    return currentPage - 3 + i;
                  }) as page}
                    <button
                      on:click={() => goToPage(page)}
                      class="relative inline-flex items-center px-4 py-2 border border-tokyo-night-fg-gutter text-sm font-medium {currentPage === page ? 'bg-tokyo-night-blue text-tokyo-night-bg border-tokyo-night-blue' : 'bg-tokyo-night-bg-light text-tokyo-night-fg hover:bg-tokyo-night-bg'}"
                    >
                      {page}
                    </button>
                  {/each}
                  <button
                    on:click={nextPage}
                    disabled={currentPage === totalPages}
                    class="relative inline-flex items-center px-2 py-2 rounded-r-md border border-tokyo-night-fg-gutter bg-tokyo-night-bg-light text-sm font-medium text-tokyo-night-fg hover:bg-tokyo-night-bg disabled:opacity-50 disabled:cursor-not-allowed"
                  >
                    →
                  </button>
                </nav>
              </div>
            </div>
          </div>
        {/if}
      </div>
    {/if}
  </div>
</div>