// Generic paginated result type for connection-based queries
export interface PaginatedResult<T> {
  items: T[];
  pageInfo: {
    hasNextPage: boolean;
    endCursor: string | null;
  };
  totalCount: number;
}

export interface UsePaginationOptions<T> {
  /** Function that fetches a page of data */
  fetchPage: (options: { first?: number; after?: string }) => Promise<PaginatedResult<T>>;
  /** Number of items to fetch per page (default: 50) */
  pageSize?: number;
  /** Whether to fetch the first page automatically (default: true) */
  autoFetch?: boolean;
}

export interface PaginationState<T> {
  /** All loaded items */
  items: T[];
  /** Whether initial load is in progress */
  loading: boolean;
  /** Whether a loadMore operation is in progress */
  loadingMore: boolean;
  /** Whether there are more pages to load */
  hasNextPage: boolean;
  /** Total count of items (if available from server) */
  totalCount: number;
  /** The cursor for fetching the next page */
  endCursor: string | null;
  /** Any error that occurred */
  error: Error | null;
}

/**
 * Creates a pagination controller for connection-based GraphQL queries.
 * Uses Svelte 5 runes for reactivity.
 *
 * @example
 * ```svelte
 * <script lang="ts">
 *   import { createPagination } from "$lib/hooks/usePagination";
 *   import { streamsService } from "$lib/graphql/services/streams";
 *
 *   const pagination = createPagination({
 *     fetchPage: (opts) => streamsService.getStreamsConnection(opts),
 *     pageSize: 25,
 *   });
 *
 *   // Access reactive state
 *   $: streams = pagination.items;
 *   $: loading = pagination.loading;
 * </script>
 *
 * {#each streams as stream}
 *   ...
 * {/each}
 *
 * {#if pagination.hasNextPage}
 *   <button onclick={pagination.loadMore}>Load More</button>
 * {/if}
 * ```
 */
export function createPagination<T>(options: UsePaginationOptions<T>) {
  const { fetchPage, pageSize = 50, autoFetch = true } = options;

  // Reactive state using Svelte 5 $state rune pattern
  // These will be reactive when used in .svelte files
  let items = $state<T[]>([]);
  let loading = $state(false);
  let loadingMore = $state(false);
  let hasNextPage = $state(true);
  let totalCount = $state(0);
  let endCursor = $state<string | null>(null);
  let error = $state<Error | null>(null);

  /**
   * Load the first page, resetting any existing data
   */
  async function load(): Promise<void> {
    loading = true;
    error = null;

    try {
      const result = await fetchPage({ first: pageSize });
      items = result.items;
      hasNextPage = result.pageInfo.hasNextPage;
      endCursor = result.pageInfo.endCursor;
      totalCount = result.totalCount;
    } catch (e) {
      error = e instanceof Error ? e : new Error(String(e));
      console.error("Pagination load error:", e);
    } finally {
      loading = false;
    }
  }

  /**
   * Load the next page and append to existing items
   */
  async function loadMore(): Promise<void> {
    if (!hasNextPage || loadingMore || loading) {
      return;
    }

    loadingMore = true;
    error = null;

    try {
      const result = await fetchPage({
        first: pageSize,
        after: endCursor ?? undefined,
      });

      items = [...items, ...result.items];
      hasNextPage = result.pageInfo.hasNextPage;
      endCursor = result.pageInfo.endCursor;
      totalCount = result.totalCount;
    } catch (e) {
      error = e instanceof Error ? e : new Error(String(e));
      console.error("Pagination loadMore error:", e);
    } finally {
      loadingMore = false;
    }
  }

  /**
   * Reset and reload from the beginning
   */
  async function reset(): Promise<void> {
    items = [];
    hasNextPage = true;
    endCursor = null;
    totalCount = 0;
    error = null;
    await load();
  }

  /**
   * Manually set items (useful for optimistic updates)
   */
  function setItems(newItems: T[]): void {
    items = newItems;
  }

  /**
   * Add an item to the beginning of the list (for newly created items)
   */
  function prepend(item: T): void {
    items = [item, ...items];
    totalCount = totalCount + 1;
  }

  /**
   * Remove an item by predicate
   */
  function remove(predicate: (item: T) => boolean): void {
    const index = items.findIndex(predicate);
    if (index !== -1) {
      items = items.filter((_, i) => i !== index);
      totalCount = Math.max(0, totalCount - 1);
    }
  }

  /**
   * Update an item in place
   */
  function update(predicate: (item: T) => boolean, updater: (item: T) => T): void {
    items = items.map((item) => (predicate(item) ? updater(item) : item));
  }

  // Auto-fetch on creation if enabled
  if (autoFetch) {
    load();
  }

  return {
    // Reactive getters (these work with Svelte 5's reactivity)
    get items() {
      return items;
    },
    get loading() {
      return loading;
    },
    get loadingMore() {
      return loadingMore;
    },
    get hasNextPage() {
      return hasNextPage;
    },
    get totalCount() {
      return totalCount;
    },
    get endCursor() {
      return endCursor;
    },
    get error() {
      return error;
    },

    // Methods
    load,
    loadMore,
    reset,
    setItems,
    prepend,
    remove,
    update,
  };
}

/**
 * Type for the return value of createPagination
 */
export type PaginationController<T> = ReturnType<typeof createPagination<T>>;
