<script>
  import { createEventDispatcher } from 'svelte';
  
  /** @type {any[]} */
  export let data = [];
  /** @type {any[]} */
  export let columns = [];
  export let loading = false;
  export let error = null;
  export let sortable = true;
  export let filterable = false;
  export let paginated = true;
  export let pageSize = 10;
  export let searchQuery = '';
  export let currentPage = 1;
  export let selectable = false;
  /** @type {any[]} */
  export let selectedItems = [];
  export let emptyMessage = 'No data available';
  export let loadingMessage = 'Loading...';
  
  const dispatch = createEventDispatcher();
  
  /** @type {any} */
  let sortBy = null;
  let sortOrder = 'asc';
  
  $: filteredData = filterData(data, searchQuery, columns);
  $: sortedData = sortData(filteredData, sortBy, sortOrder);
  $: paginatedData = paginated ? paginateData(sortedData, currentPage, pageSize) : sortedData;
  $: totalPages = paginated ? Math.ceil(sortedData.length / pageSize) : 1;
  $: totalItems = sortedData.length;
  $: startIndex = paginated ? (currentPage - 1) * pageSize + 1 : 1;
  $: endIndex = paginated ? Math.min(currentPage * pageSize, totalItems) : totalItems;
  
  /**
   * @param {any[]} items
   * @param {string} query
   * @param {any[]} columns
   * @returns {any[]}
   */
  function filterData(items, query, columns) {
    if (!query || !filterable) return items;
    
    const searchTerm = query.toLowerCase();
    return items.filter(item => {
      return columns.some(column => {
        const value = getNestedValue(item, column.key);
        return value && value.toString().toLowerCase().includes(searchTerm);
      });
    });
  }
  
  /**
   * @param {any[]} items
   * @param {any} column
   * @param {string} order
   * @returns {any[]}
   */
  function sortData(items, column, order) {
    if (!column || !sortable) return items;
    
    return [...items].sort((a, b) => {
      const aVal = getNestedValue(a, column);
      const bVal = getNestedValue(b, column);
      
      if (aVal === null || aVal === undefined) return 1;
      if (bVal === null || bVal === undefined) return -1;
      
      let comparison = 0;
      if (typeof aVal === 'number' && typeof bVal === 'number') {
        comparison = aVal - bVal;
      } else {
        comparison = aVal.toString().localeCompare(bVal.toString());
      }
      
      return order === 'desc' ? -comparison : comparison;
    });
  }
  
  /**
   * @param {any[]} items
   * @param {number} page
   * @param {number} size
   * @returns {any[]}
   */
  function paginateData(items, page, size) {
    const start = (page - 1) * size;
    const end = start + size;
    return items.slice(start, end);
  }
  
  /**
   * @param {any} obj
   * @param {string} key
   * @returns {any}
   */
  function getNestedValue(obj, key) {
    return key.split('.').reduce((o, k) => o?.[k], obj);
  }
  
  /**
   * @param {any} column
   */
  function handleSort(column) {
    if (!sortable || !column.sortable) return;
    
    if (sortBy === column.key) {
      sortOrder = sortOrder === 'asc' ? 'desc' : 'asc';
    } else {
      sortBy = column.key;
      sortOrder = 'asc';
    }
    
    dispatch('sort', { column: column.key, order: sortOrder });
  }
  
  /**
   * @param {any} item
   * @param {boolean} checked
   */
  function handleSelect(item, checked) {
    if (checked) {
      selectedItems = [...selectedItems, item];
    } else {
      selectedItems = selectedItems.filter(selected => selected !== item);
    }
    
    dispatch('select', { item, selected: checked, selectedItems });
  }
  
  /**
   * @param {boolean} checked
   */
  function handleSelectAll(checked) {
    selectedItems = checked ? [...paginatedData] : [];
    dispatch('selectAll', { selected: checked, selectedItems });
  }
  
  /**
   * @param {number} page
   */
  function goToPage(page) {
    if (page >= 1 && page <= totalPages) {
      currentPage = page;
      dispatch('pageChange', { page });
    }
  }
  
  function nextPage() {
    if (currentPage < totalPages) {
      goToPage(currentPage + 1);
    }
  }
  
  function prevPage() {
    if (currentPage > 1) {
      goToPage(currentPage - 1);
    }
  }
</script>

<div class="bg-slate-800 rounded-lg border border-slate-700 overflow-hidden">
  <!-- Search bar -->
  {#if filterable}
    <div class="p-4 border-b border-slate-700">
      <input
        type="text"
        bind:value={searchQuery}
        placeholder="Search..."
        class="w-full px-3 py-2 bg-slate-700 border border-slate-600 rounded-md text-slate-100 placeholder-slate-400 focus:outline-none focus:ring-2 focus:ring-blue-500 focus:border-transparent"
      />
    </div>
  {/if}

  {#if loading}
    <div class="flex justify-center items-center py-12">
      <div class="animate-spin rounded-full h-8 w-8 border-b-2 border-blue-500"></div>
      <span class="ml-3 text-slate-400">{loadingMessage}</span>
    </div>
  {:else if error}
    <div class="p-6 text-center">
      <div class="text-red-400 mb-2">Error</div>
      <div class="text-slate-400">{error}</div>
    </div>
  {:else if paginatedData.length === 0}
    <div class="p-12 text-center text-slate-400">
      {emptyMessage}
    </div>
  {:else}
    <!-- Table -->
    <div class="overflow-x-auto">
      <table class="w-full">
        <thead class="bg-slate-700">
          <tr>
            {#if selectable}
              <th class="px-6 py-3 text-left">
                <input
                  type="checkbox"
                  checked={selectedItems.length === paginatedData.length && paginatedData.length > 0}
                  indeterminate={selectedItems.length > 0 && selectedItems.length < paginatedData.length}
                  on:change={(e) => handleSelectAll(/** @type {HTMLInputElement} */ (e.target).checked)}
                  class="rounded border-slate-600 bg-slate-700 text-blue-600 focus:ring-blue-500"
                />
              </th>
            {/if}
            
            {#each columns as column}
              <th 
                class="px-6 py-3 text-left text-xs font-medium text-slate-300 uppercase tracking-wider {column.sortable !== false && sortable ? 'cursor-pointer hover:text-slate-100' : ''}"
                on:click={() => handleSort(column)}
              >
                <div class="flex items-center">
                  {column.title || column.key}
                  {#if column.sortable !== false && sortable}
                    <span class="ml-1 text-slate-500">
                      {#if sortBy === column.key}
                        {sortOrder === 'asc' ? '↑' : '↓'}
                      {:else}
                        ↕
                      {/if}
                    </span>
                  {/if}
                </div>
              </th>
            {/each}
          </tr>
        </thead>
        
        <tbody class="divide-y divide-slate-700">
          {#each paginatedData as item, i (item.id || i)}
            <tr class="hover:bg-slate-700/50 transition-colors">
              {#if selectable}
                <td class="px-6 py-4">
                  <input
                    type="checkbox"
                    checked={selectedItems.includes(item)}
                    on:change={(e) => handleSelect(item, /** @type {HTMLInputElement} */ (e.target).checked)}
                    class="rounded border-slate-600 bg-slate-700 text-blue-600 focus:ring-blue-500"
                  />
                </td>
              {/if}
              
              {#each columns as column}
                <td class="px-6 py-4 text-sm text-slate-300 {column.class || ''}">
                  {#if column.component}
                    <svelte:component this={column.component} value={getNestedValue(item, column.key)} {item} {column} />
                  {:else if column.render}
                    {@html column.render(getNestedValue(item, column.key), item)}
                  {:else}
                    {getNestedValue(item, column.key) || '—'}
                  {/if}
                </td>
              {/each}
            </tr>
          {/each}
        </tbody>
      </table>
    </div>

    <!-- Pagination -->
    {#if paginated && totalPages > 1}
      <div class="bg-slate-700 px-6 py-3 flex items-center justify-between border-t border-slate-600">
        <div class="flex-1 flex justify-between sm:hidden">
          <button
            on:click={prevPage}
            disabled={currentPage === 1}
            class="relative inline-flex items-center px-4 py-2 border border-slate-600 text-sm font-medium rounded-md text-slate-300 bg-slate-800 hover:bg-slate-700 disabled:opacity-50 disabled:cursor-not-allowed"
          >
            Previous
          </button>
          <button
            on:click={nextPage}
            disabled={currentPage === totalPages}
            class="ml-3 relative inline-flex items-center px-4 py-2 border border-slate-600 text-sm font-medium rounded-md text-slate-300 bg-slate-800 hover:bg-slate-700 disabled:opacity-50 disabled:cursor-not-allowed"
          >
            Next
          </button>
        </div>
        
        <div class="hidden sm:flex-1 sm:flex sm:items-center sm:justify-between">
          <div>
            <p class="text-sm text-slate-400">
              Showing
              <span class="font-medium">{startIndex}</span>
              to
              <span class="font-medium">{endIndex}</span>
              of
              <span class="font-medium">{totalItems}</span>
              results
            </p>
          </div>
          
          <div>
            <nav class="relative z-0 inline-flex rounded-md shadow-sm -space-x-px">
              <button
                on:click={prevPage}
                disabled={currentPage === 1}
                class="relative inline-flex items-center px-2 py-2 rounded-l-md border border-slate-600 bg-slate-800 text-sm font-medium text-slate-300 hover:bg-slate-700 disabled:opacity-50 disabled:cursor-not-allowed"
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
                  class="relative inline-flex items-center px-4 py-2 border border-slate-600 text-sm font-medium {currentPage === page ? 'bg-blue-600 text-white border-blue-500' : 'bg-slate-800 text-slate-300 hover:bg-slate-700'}"
                >
                  {page}
                </button>
              {/each}
              
              <button
                on:click={nextPage}
                disabled={currentPage === totalPages}
                class="relative inline-flex items-center px-2 py-2 rounded-r-md border border-slate-600 bg-slate-800 text-sm font-medium text-slate-300 hover:bg-slate-700 disabled:opacity-50 disabled:cursor-not-allowed"
              >
                →
              </button>
            </nav>
          </div>
        </div>
      </div>
    {/if}
  {/if}
</div>