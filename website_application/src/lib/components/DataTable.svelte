<script lang="ts" generics="T extends Row">
  import { createEventDispatcher } from "svelte";
  import type { ComponentType } from "svelte";
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
  import { ArrowDownIcon, ArrowUpIcon, ChevronsUpDownIcon, Loader2Icon } from "lucide-svelte";

  type Row = Record<string, unknown>;

  interface Column {
    key: string;
    title?: string;
    sortable?: boolean;
    class?: string;
    component?: ComponentType;
    render?: (value: unknown, item: Row) => string;
  }

  interface Props<T extends Row> {
    data?: T[];
    columns?: Column[];
    loading?: boolean;
    error?: unknown;
    sortable?: boolean;
    filterable?: boolean;
    paginated?: boolean;
    pageSize?: number;
    searchQuery?: string;
    currentPage?: number;
    selectable?: boolean;
    selectedItems?: T[];
    emptyMessage?: string;
    loadingMessage?: string;
  }


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
  }: Props<T> = $props();

  const dispatch = createEventDispatcher();
  const searchInputId = `data-table-search-${Math.random().toString(36).slice(2, 8)}`;

  let sortBy = $state<string | null>(null);
  let sortOrder = $state<"asc" | "desc">("asc");

  function filterData<T extends Row>(items: T[], query: string, columns: Column[]): T[] {
    if (!query || !filterable) return items;

    const searchTerm = query.toLowerCase();
    return items.filter((item: T) => {
      return columns.some((column) => {
        const value = getNestedValue(item, column.key);
        return value && value.toString().toLowerCase().includes(searchTerm);
      });
    });
  }

  function sortData<T extends Row>(items: T[], column: string | null, order: "asc" | "desc"): T[] {
    if (!column || !sortable) return items;

    return [...items].sort((a: T, b: T) => {
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

  function paginateData<T extends Row>(items: T[], page: number, size: number): T[] {
    const start = (page - 1) * size;
    const end = start + size;
    return items.slice(start, end);
  }

  function getNestedValue<T extends Row>(obj: T, key: string): unknown {
    return key.split(".").reduce((o: Record<string, any> | undefined, k) => o?.[k], obj as Record<string, any>);
  }

  function handleSort(column: Column) {
    if (!sortable || !column.sortable) return;

    if (sortBy === column.key) {
      sortOrder = sortOrder === "asc" ? "desc" : "asc";
    } else {
      sortBy = column.key;
      sortOrder = "asc";
    }

    dispatch("sort", { column: column.key, order: sortOrder });
  }

  function handleSelect(item: T, checked: boolean) {
    if (checked) {
      selectedItems = [...selectedItems, item];
    } else {
      selectedItems = selectedItems.filter((selected) => selected !== item);
    }

    dispatch("select", { item, selected: checked, selectedItems });
  }

  function handleSelectAll(checked: boolean) {
    selectedItems = checked ? ([...paginatedData] as T[]) : [];
    dispatch("selectAll", { selected: checked, selectedItems });
  }

  function goToPage(page: number) {
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

  function buildVisiblePages(total: number, current: number): number[] {
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
