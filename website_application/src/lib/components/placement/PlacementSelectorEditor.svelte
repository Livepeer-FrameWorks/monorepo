<script lang="ts">
  import { onDestroy } from "svelte";
  import { GetMediaPlacementOptionsStore } from "$houdini";
  import type { GetMediaPlacementOptions$result } from "$houdini";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { copySelector, type Scope, type Selector } from "$lib/placement/model";

  let {
    value,
    scope,
    disabled = false,
    onchange,
  }: {
    value: Selector;
    scope: Scope;
    disabled?: boolean;
    onchange: (value: Selector) => void;
  } = $props();
  type Connection = Extract<
    GetMediaPlacementOptions$result["mediaPlacementOptions"],
    { __typename: "MediaPlacementOptionsConnection" }
  >;
  type ListField = "clusterIds" | "nodeIds" | "ownerIds" | "regions";
  const listFields: { field: ListField; label: string }[] = [
    { field: "clusterIds", label: "Cluster" },
    { field: "nodeIds", label: "Node" },
    { field: "ownerIds", label: "Operator" },
    { field: "regions", label: "Region" },
  ];
  let kind = $state<"CLUSTER" | "NODE" | "OPERATOR" | "REGION">("CLUSTER");
  let query = $state("");
  let options = $state<Connection["nodes"]>([]);
  let cursor = $state<string | null>(null);
  let hasNext = $state(false);
  let loading = $state(false);
  let error = $state("");
  let searched = $state(false);
  let sequence = 0;
  const categories = [
    { value: "TENANT_PRIVATE" as const, label: "My clusters" },
    { value: "THIRD_PARTY_MARKETPLACE" as const, label: "Connected marketplace" },
    { value: "PLATFORM_OFFICIAL" as const, label: "Official clusters" },
  ];
  const selectedField = $derived<ListField>(
    kind === "CLUSTER"
      ? "clusterIds"
      : kind === "NODE"
        ? "nodeIds"
        : kind === "OPERATOR"
          ? "ownerIds"
          : "regions"
  );
  onDestroy(() => {
    sequence++;
  });

  function toggle(field: "classes" | "charging", entry: string, checked: boolean) {
    const next = copySelector(value);
    if (field === "classes") {
      const item = categories.find((category) => category.value === entry)?.value;
      if (!item) return;
      next.classes = checked
        ? [...new Set([...(next.classes ?? []), item])]
        : next.classes?.filter((item) => item !== entry);
    } else {
      const item: "RATED" | "PERMANENTLY_FREE" = entry === "RATED" ? "RATED" : "PERMANENTLY_FREE";
      next.charging = checked
        ? [...new Set([...(next.charging ?? []), item])]
        : next.charging?.filter((item) => item !== entry);
    }
    onchange(next);
  }

  function selectOption(id: string) {
    const next = copySelector(value);
    next[selectedField] = [...new Set([...(next[selectedField] ?? []).map(String), id])];
    onchange(next);
  }

  function remove(field: ListField, id: string | number) {
    const next = copySelector(value);
    next[field] = (next[field] ?? []).filter((item) => item !== id) as string[];
    onchange(next);
  }

  function invalidateSearch() {
    sequence++;
    clearResults();
    loading = false;
    error = "";
  }

  function clearResults() {
    options = [];
    cursor = null;
    hasNext = false;
    searched = false;
  }

  function optionStatus(option: Connection["nodes"][number]) {
    if (option.eligible) return "Connected";
    switch (option.reason) {
      case "owner_consent_unknown":
        return "Owner permissions could not be verified";
      case "owner_denies_media":
        return "Owner does not currently allow ingest or viewer delivery";
      default:
        return "No currently eligible media target";
    }
  }

  async function search(more = false) {
    const request = ++sequence;
    loading = true;
    error = "";
    try {
      const result = await new GetMediaPlacementOptionsStore().fetch({
        policy: "NetworkOnly",
        variables: { scope, filter: { kind, query }, first: 30, after: more ? cursor : null },
      });
      if (request !== sequence) return;
      const data = result.data?.mediaPlacementOptions;
      if (data?.__typename === "MediaPlacementOptionsConnection") {
        options = more
          ? [
              ...options,
              ...data.nodes.filter((item) => !options.some((existing) => existing.id === item.id)),
            ]
          : data.nodes;
        cursor = data.pageInfo.endCursor;
        hasNext = data.pageInfo.hasNextPage;
        searched = true;
      } else {
        clearResults();
        error =
          data?.__typename === "MediaPlacementError" && data.code === "REVISION_CONFLICT"
            ? "Available options changed. Search again to refresh the list."
            : data && "message" in data
              ? data.message
              : "Options are unavailable.";
      }
    } catch {
      if (request === sequence) {
        clearResults();
        error = "Could not load authorized options.";
      }
    } finally {
      if (request === sequence) loading = false;
    }
  }
</script>

<fieldset {disabled} class="space-y-3 border-l-2 border-border pl-3">
  <legend class="text-sm font-medium mb-2">Matching capacity</legend>
  <div class="flex flex-wrap gap-3">
    {#each categories as category (category.value)}
      <label class="flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          checked={value.classes?.includes(category.value) ?? false}
          onchange={(event) => toggle("classes", category.value, event.currentTarget.checked)}
        />
        {category.label}
      </label>
    {/each}
  </div>
  <div class="flex flex-wrap gap-3">
    {#each [{ value: "RATED", label: "Rated" }, { value: "PERMANENTLY_FREE", label: "Permanently free" }] as charging (charging.value)}
      <label class="flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          checked={value.charging?.some((item) => item === charging.value) ?? false}
          onchange={(event) => toggle("charging", charging.value, event.currentTarget.checked)}
        />
        {charging.label}
      </label>
    {/each}
  </div>
  <p class="text-xs text-muted-foreground">
    Values within one field match any; different fields must all match. Empty fields add no
    restriction. Included or temporarily waived usage is not permanently free.
  </p>
  {#each listFields as { field, label } (field)}
    {#each value[field] ?? [] as id (id)}
      <div class="flex items-center justify-between gap-2 text-xs">
        <span class="break-all">{label}: {options.find((item) => item.id === id)?.name ?? id}</span>
        <Button
          size="sm"
          variant="ghost"
          onclick={() => remove(field, id)}
          aria-label={`Remove ${id}`}>Remove</Button
        >
      </div>
    {/each}
  {/each}
  <details>
    <summary class="text-sm cursor-pointer"
      >Choose specific clusters, nodes, operators or regions</summary
    >
    <div class="space-y-2 pt-3">
      <label class="block text-sm"
        >Option type
        <select
          class="block w-full border border-border bg-background p-2"
          bind:value={kind}
          onchange={invalidateSearch}
        >
          <option value="CLUSTER">Clusters</option><option value="NODE">Nodes of my clusters</option
          ><option value="OPERATOR">Operators</option><option value="REGION">Regions</option>
        </select>
      </label>
      {#if kind === "NODE"}
        <p class="text-xs text-muted-foreground">
          Only nodes of clusters you own can be named. A node rule matches nothing on other
          clusters.
        </p>
      {/if}
      <Input
        aria-label="Search authorized capacity"
        bind:value={query}
        oninput={invalidateSearch}
        placeholder="Search connected capacity"
      />
      <Button size="sm" variant="outline" onclick={() => search()} disabled={loading}>Search</Button
      >
      {#if error}<p role="alert" class="text-sm text-destructive">{error}</p>{/if}
      {#if searched && options.length === 0}
        <p role="status" class="text-sm text-muted-foreground">
          No authorized options match your search.
        </p>
      {/if}
      {#each options as option (option.id)}
        <div class="flex items-start justify-between gap-2 border-t border-border py-2 text-sm">
          <div>
            {option.name}
            <p class="text-xs text-muted-foreground">
              {option.kind === "NODE" && option.clusterId ? `Cluster ${option.clusterId}` : ""}
              {option.region ?? ""}
              {optionStatus(option)}
            </p>
          </div>
          <Button
            size="sm"
            variant="outline"
            disabled={value[selectedField]?.includes(option.id)}
            onclick={() => selectOption(option.id)}>Add</Button
          >
        </div>
      {/each}
      {#if hasNext}<Button
          size="sm"
          variant="outline"
          disabled={loading}
          onclick={() => search(true)}>Load more</Button
        >{/if}
      <p class="text-xs text-muted-foreground">
        Only authorized options are listed. Selecting one never subscribes to marketplace capacity.
        Connected does not guarantee available capacity or permission for both ingest and delivery.
      </p>
    </div>
  </details>
</fieldset>
