<script>
  import { createEventDispatcher } from "svelte";
  import {
    Card,
    CardContent,
    CardFooter,
    CardHeader,
  } from "$lib/components/ui/card";
  import { Input } from "$lib/components/ui/input";
  import { Button } from "$lib/components/ui/button";
  import { Checkbox } from "$lib/components/ui/checkbox";
  import { Label } from "$lib/components/ui/label";
  import {
    Table,
    TableBody,
    TableCell,
    TableHead,
    TableHeader,
    TableRow,
  } from "$lib/components/ui/table";
  import { ArrowDownIcon } from "lucide-svelte";
  import { ArrowUpIcon } from "lucide-svelte";
  import { ChevronsUpDownIcon } from "lucide-svelte";
  import { Loader2Icon } from "lucide-svelte";

  /**
   * @template Row extends Record<string, unknown>
   * @typedef {object} Column
   * @property {string} key
   * @property {string} [title]
   * @property {boolean} [sortable]
   * @property {string} [class]
   * @property {import('svelte').ComponentType} [component]
   * @property {(value: unknown, item: Row) => string} [render]
   */

  /**
   * @typedef {Object} Props
   * @property {Array<Record<string, unknown>>} [data]
   * @property {Array<Column<Record<string, unknown>>>} [columns]
   * @property {boolean} [loading]
   * @property {unknown} [error]
   * @property {boolean} [sortable]
   * @property {boolean} [filterable]
   * @property {boolean} [paginated]
   * @property {number} [pageSize]
   * @property {string} [searchQuery]
   * @property {number} [currentPage]
   * @property {boolean} [selectable]
   * @property {Array<Record<string, unknown>>} [selectedItems]
   * @property {string} [emptyMessage]
   * @property {string} [loadingMessage]
   */

  /** @type {Props} */
  let {
    data = [],
    columns = [],
    loading = false,
    error = null,
    sortable = true,
    filterable = false,
    paginated = true,
    pageSize = 10,
    searchQuery = $bindable(""),
    currentPage = $bindable(1),
    selectable = false,
    selectedItems = $bindable([]),
    emptyMessage = "No data available",
    loadingMessage = "Loading...",
  } = $props();

  const dispatch = createEventDispatcher();
  const searchInputId = `data-table-search-${Math.random().toString(36).slice(2, 8)}`;

  /** @type {string | null} */
  let sortBy = $state(null);
  /** @type {"asc"|"desc"} */
  let sortOrder = $state("asc");

  /**
   * @param {Array<Record<string, unknown>>} items
   * @param {string} query
   * @param {Array<Column<Record<string, unknown>>>} columns
   * @returns {Array<Record<string, unknown>>}
   */
  function filterData(items, query, columns) {
    if (!query || !filterable) return items;

    const searchTerm = query.toLowerCase();
    return items.filter((item) => {
      return columns.some((column) => {
        const value = getNestedValue(item, column.key);
        return value && value.toString().toLowerCase().includes(searchTerm);
      });
    });
  }

  /**
   * @param {Array<Record<string, unknown>>} items
   * @param {string | null} column
   * @param {"asc"|"desc"} order
   * @returns {Array<Record<string, unknown>>}
   */
  function sortData(items, column, order) {
    if (!column || !sortable) return items;

    return [...items].sort((a, b) => {
      const aVal = getNestedValue(a, column);
      const bVal = getNestedValue(b, column);

      if (aVal === null || aVal === undefined) return 1;
      if (bVal === null || bVal === undefined) return -1;

      let comparison = 0;
      if (typeof aVal === "number" && typeof bVal === "number") {
        comparison = aVal - bVal;
      } else {
        comparison = aVal.toString().localeCompare(bVal.toString());
      }

      return order === "desc" ? -comparison : comparison;
    });
  }

  /**
   * @param {Array<Record<string, unknown>>} items
   * @param {number} page
   * @param {number} size
   * @returns {Array<Record<string, unknown>>}
   */
  function paginateData(items, page, size) {
    const start = (page - 1) * size;
    const end = start + size;
    return items.slice(start, end);
  }

  /**
   * @param {Record<string, unknown>} obj
   * @param {string} key
   * @returns {unknown}
   */
  function getNestedValue(obj, key) {
    return key.split(".").reduce((o, k) => o?.[k], obj);
  }

  /**
   * @param {Column<Record<string, unknown>>} column
   */
  function handleSort(column) {
    if (!sortable || !column.sortable) return;

    if (sortBy === column.key) {
      sortOrder = sortOrder === "asc" ? "desc" : "asc";
    } else {
      sortBy = column.key;
      sortOrder = "asc";
    }

    dispatch("sort", { column: column.key, order: sortOrder });
  }

  /**
   * @param {Record<string, unknown>} item
   * @param {boolean} checked
   */
  function handleSelect(item, checked) {
    if (checked) {
      selectedItems = [...selectedItems, item];
    } else {
      selectedItems = selectedItems.filter((selected) => selected !== item);
    }

    dispatch("select", { item, selected: checked, selectedItems });
  }

  /**
   * @param {boolean} checked
   */
  function handleSelectAll(checked) {
    selectedItems = checked ? [...paginatedData] : [];
    dispatch("selectAll", { selected: checked, selectedItems });
  }

  /**
   * @param {number} page
   */
  function goToPage(page) {
    if (page >= 1 && page <= totalPages) {
      currentPage = page;
      dispatch("pageChange", { page });
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

  /**
   * @param {number} total
   * @param {number} current
   * @returns {number[]}
   */
  function buildVisiblePages(total, current) {
    if (total <= 7) return Array.from({ length: total }, (_, i) => i + 1);
    if (current <= 4) return Array.from({ length: 7 }, (_, i) => i + 1);
    if (current >= total - 3)
      return Array.from({ length: 7 }, (_, i) => total - 6 + i);
    return Array.from({ length: 7 }, (_, i) => current - 3 + i);
  }

  let filteredData = $derived(filterData(data, searchQuery, columns));
  let sortedData = $derived(sortData(filteredData, sortBy, sortOrder));
  let paginatedData = $derived(
    paginated ? paginateData(sortedData, currentPage, pageSize) : sortedData,
  );
  let totalPages = $derived(
    paginated ? Math.ceil(sortedData.length / pageSize) : 1,
  );
  let totalItems = $derived(sortedData.length);
  let startIndex = $derived(paginated ? (currentPage - 1) * pageSize + 1 : 1);
  let endIndex = $derived(
    paginated ? Math.min(currentPage * pageSize, totalItems) : totalItems,
  );
  let allRowsSelected = $derived(
    selectable &&
      paginatedData.length > 0 &&
      selectedItems.length === paginatedData.length,
  );
  let someRowsSelected = $derived(
    selectable &&
      selectedItems.length > 0 &&
      selectedItems.length < paginatedData.length,
  );
  let visiblePages = $derived(buildVisiblePages(totalPages, currentPage));
</script>

<Card class="glass-card overflow-hidden">
  {#if filterable}
    <CardHeader class="gap-3 border-b border-border/60 px-6 py-4">
      <div
        class="flex w-full flex-col gap-3 sm:flex-row sm:items-center sm:justify-between"
      >
        <div class="w-full sm:max-w-sm">
          <Label class="sr-only" for={searchInputId}>Search</Label>
          <Input
            id={searchInputId}
            type="search"
            bind:value={searchQuery}
            placeholder="Search..."
            class="w-full"
          />
        </div>
      </div>
    </CardHeader>
  {/if}

  {#if loading}
    <CardContent class="flex items-center justify-center gap-3 py-12">
      <Loader2Icon
        class="size-6 animate-spin text-primary"
        aria-hidden="true"
      />
      <span class="text-muted-foreground">{loadingMessage}</span>
    </CardContent>
  {:else if error}
    <CardContent class="py-10 text-center">
      <p class="mb-1 text-destructive">Error</p>
      <p class="text-muted-foreground">{error}</p>
    </CardContent>
  {:else if paginatedData.length === 0}
    <CardContent class="py-12 text-center text-muted-foreground">
      {emptyMessage}
    </CardContent>
  {:else}
    <CardContent class="p-0">
      <Table class="min-w-full">
        <TableHeader>
          <TableRow>
            {#if selectable}
              <TableHead class="w-12">
                <Checkbox
                  checked={allRowsSelected}
                  indeterminate={someRowsSelected}
                  aria-label="Select all rows"
                  onclick={() => handleSelectAll(!allRowsSelected)}
                />
              </TableHead>
            {/if}

            {#each columns as column (column.key)}
              <TableHead
                class={`whitespace-nowrap ${column.class ?? ""} ${
                  column.sortable !== false && sortable
                    ? "cursor-pointer select-none"
                    : ""
                }`}
                onclick={() => handleSort(column)}
              >
                <span class="inline-flex items-center gap-1">
                  {column.title || column.key}
                  {#if column.sortable !== false && sortable}
                    {#if sortBy === column.key}
                      {#if sortOrder === "asc"}
                        <ArrowUpIcon class="size-3.5" aria-hidden="true" />
                      {:else}
                        <ArrowDownIcon class="size-3.5" aria-hidden="true" />
                      {/if}
                    {:else}
                      <ChevronsUpDownIcon
                        class="size-3.5 text-muted-foreground"
                        aria-hidden="true"
                      />
                    {/if}
                  {/if}
                </span>
              </TableHead>
            {/each}
          </TableRow>
        </TableHeader>

        <TableBody>
          {#each paginatedData as item, i (item.id || i)}
            <TableRow
              class={selectedItems.includes(item) ? "bg-muted/40" : undefined}
            >
              {#if selectable}
                <TableCell class="w-12 align-middle">
                  <Checkbox
                    checked={selectedItems.includes(item)}
                    aria-label={`Select row ${i + 1}`}
                    onclick={() =>
                      handleSelect(item, !selectedItems.includes(item))}
                  />
                </TableCell>
              {/if}

              {#each columns as column (column.key)}
                <TableCell class={`align-middle text-sm ${column.class ?? ""}`}>
                  {#if column.component}
                    <column.component
                      value={getNestedValue(item, column.key)}
                      {item}
                      {column}
                    />
                  {:else if column.render}
                    <!-- eslint-disable-next-line svelte/no-at-html-tags -->
                    {@html column.render(
                      getNestedValue(item, column.key),
                      item,
                    )}
                  {:else}
                    {getNestedValue(item, column.key) ?? "—"}
                  {/if}
                </TableCell>
              {/each}
            </TableRow>
          {/each}
        </TableBody>
      </Table>
    </CardContent>

    {#if paginated && totalPages > 1}
      <CardFooter
        class="flex flex-col gap-4 border-t border-border/60 px-6 py-4 sm:flex-row sm:items-center sm:justify-between"
      >
        <p class="text-sm text-muted-foreground">
          Showing <span class="font-medium text-foreground">{startIndex}</span>
          to <span class="font-medium text-foreground">{endIndex}</span>
          of <span class="font-medium text-foreground">{totalItems}</span> results
        </p>

        <div class="flex items-center gap-2">
          <Button
            variant="outline"
            size="sm"
            onclick={prevPage}
            disabled={currentPage === 1}
            aria-label="Previous page"
          >
            ←
          </Button>

          {#each visiblePages as page (page)}
            <Button
              size="sm"
              variant={currentPage === page ? "default" : "outline"}
              onclick={() => goToPage(page)}
              aria-current={currentPage === page ? "page" : undefined}
            >
              {page}
            </Button>
          {/each}

          <Button
            variant="outline"
            size="sm"
            onclick={nextPage}
            disabled={currentPage === totalPages}
            aria-label="Next page"
          >
            →
          </Button>
        </div>
      </CardFooter>
    {/if}
  {/if}
</Card>
