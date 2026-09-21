<script lang="ts">
  import { onMount } from "svelte";
  import {
    GetWebhookDeliveriesConnectionStore,
    GetWebhookDeliveryStore,
    ReplayWebhookDeliveriesStore,
    ReplayWebhookDeliveryStore,
  } from "$houdini";
  import { Badge } from "$lib/components/ui/badge";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Label } from "$lib/components/ui/label";
  import {
    Table,
    TableBody,
    TableCell,
    TableHead,
    TableHeader,
    TableRow,
  } from "$lib/components/ui/table";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import { toast } from "$lib/stores/toast";
  import {
    RANGE_REPLAY_BATCH,
    attemptOutcome,
    deliveryFilterActive,
    deliveryQueryVariables,
    deliveryStatusClass,
    deliveryStatusLabel,
    emptyDeliveryFilter,
    formatWebhookTime,
    isoToLocalInput,
    mutationOutcome,
    rangeReplayMessage,
    rangeReplayPlan,
    webhookDeliveryStatuses,
    type DeliveryFilter,
    type WebhookAttemptRow,
    type WebhookDeliveryRow,
    type WebhookDeliveryStatus,
  } from "$lib/webhooks";

  let {
    endpointId,
    endpointEnabled,
    eventTypes,
  }: {
    endpointId: string;
    endpointEnabled: boolean;
    eventTypes: readonly string[];
  } = $props();

  const PAGE_SIZE = 50;

  const deliveriesStore = new GetWebhookDeliveriesConnectionStore();
  const deliveryStore = new GetWebhookDeliveryStore();
  const replayStore = new ReplayWebhookDeliveryStore();
  const rangeReplayStore = new ReplayWebhookDeliveriesStore();

  let filter = $state<DeliveryFilter>({ ...emptyDeliveryFilter, statuses: [] });
  let deliveries = $state.raw<WebhookDeliveryRow[]>([]);
  let totalCount = $state(0);
  let endCursor = $state<string | null>(null);
  let hasNextPage = $state(false);
  let loading = $state(true);
  let loadingMore = $state(false);
  let loadError = $state("");
  let request = 0;

  let expandedId = $state<string | null>(null);
  let attempts = $state.raw<WebhookAttemptRow[]>([]);
  let attemptsLoading = $state(false);
  let attemptsError = $state("");
  let replayingId = $state<string | null>(null);

  let rangeAfter = $state(isoToLocalInput(new Date(Date.now() - 24 * 60 * 60 * 1000)));
  let rangeBefore = $state(isoToLocalInput(new Date(Date.now() + 60 * 1000)));
  let rangeReplaying = $state(false);
  let rangeResult = $state("");

  const filtered = $derived(deliveryFilterActive(filter));
  const specificTypes = $derived(eventTypes.filter((type) => type !== "*"));

  async function load(mode: "reset" | "more" = "reset") {
    const current = ++request;
    if (mode === "more") loadingMore = true;
    else loading = true;
    loadError = "";
    try {
      const result = await deliveriesStore.fetch({
        policy: "NetworkOnly",
        variables: {
          ...deliveryQueryVariables(endpointId, filter),
          first: PAGE_SIZE,
          after: mode === "more" ? endCursor : null,
        },
      });
      if (current !== request) return;
      const connection = result.data?.webhookDeliveriesConnection;
      if (!connection) {
        loadError = result.errors?.[0]?.message ?? "Could not load deliveries.";
        if (mode === "reset") deliveries = [];
        return;
      }
      const rows = connection.edges.map((edge) => edge.node);
      deliveries =
        mode === "more"
          ? [...deliveries, ...rows.filter((row) => !deliveries.some((item) => item.id === row.id))]
          : rows;
      totalCount = connection.totalCount;
      endCursor = connection.pageInfo.endCursor;
      hasNextPage = connection.pageInfo.hasNextPage;
    } catch {
      if (current === request) loadError = "Could not load deliveries.";
    } finally {
      if (current === request) {
        loading = false;
        loadingMore = false;
      }
    }
  }

  onMount(() => {
    void load();
  });

  /** Reloads the log; the parent calls it after a test delivery. */
  export function refresh() {
    void load();
  }

  function setStatuses(next: WebhookDeliveryStatus[]) {
    filter = { ...filter, statuses: next };
    void load();
  }

  function toggleStatus(status: WebhookDeliveryStatus) {
    setStatuses(
      filter.statuses.includes(status)
        ? filter.statuses.filter((item) => item !== status)
        : [...filter.statuses, status]
    );
  }

  function clearFilters() {
    filter = { ...emptyDeliveryFilter, statuses: [] };
    void load();
  }

  async function toggleAttempts(id: string) {
    if (expandedId === id) {
      expandedId = null;
      return;
    }
    expandedId = id;
    attempts = [];
    attemptsError = "";
    attemptsLoading = true;
    try {
      const result = await deliveryStore.fetch({ variables: { id }, policy: "NetworkOnly" });
      if (expandedId !== id) return;
      const delivery = result.data?.webhookDelivery;
      if (!delivery) {
        attemptsError = result.errors?.[0]?.message ?? "This delivery is no longer available.";
        return;
      }
      attempts = delivery.attemptHistory;
    } catch {
      if (expandedId === id) attemptsError = "Could not load the attempts.";
    } finally {
      if (expandedId === id) attemptsLoading = false;
    }
  }

  async function replay(id: string) {
    replayingId = id;
    try {
      const result = await replayStore.mutate({ id });
      const outcome = mutationOutcome(
        result.data?.replayWebhookDelivery,
        result.errors,
        "WebhookDelivery",
        "The delivery was not replayed."
      );
      if (!outcome.ok) {
        toast.error(outcome.message);
        return;
      }
      toast.success("Delivery queued again under the same ID");
      await load();
    } catch {
      toast.error("The delivery was not replayed.");
    } finally {
      replayingId = null;
    }
  }

  async function replayRange(event: SubmitEvent) {
    event.preventDefault();
    const plan = rangeReplayPlan(endpointId, rangeAfter, rangeBefore);
    if (!plan.ok) {
      rangeResult = plan.message;
      return;
    }
    rangeReplaying = true;
    rangeResult = "";
    try {
      const result = await rangeReplayStore.mutate(plan.variables);
      const outcome = mutationOutcome(
        result.data?.replayWebhookDeliveries,
        result.errors,
        "WebhookReplayResult",
        "The deliveries were not replayed."
      );
      if (!outcome.ok) {
        rangeResult = outcome.message;
        return;
      }
      const value = outcome.value as { replayedCount?: number; hasMore?: boolean };
      rangeResult = rangeReplayMessage(value.replayedCount ?? 0, value.hasMore ?? false);
      await load();
    } catch {
      rangeResult = "The deliveries were not replayed.";
    } finally {
      rangeReplaying = false;
    }
  }

  function replayable(delivery: WebhookDeliveryRow) {
    return endpointEnabled && delivery.kind === "EVENT" && delivery.status !== "PENDING";
  }

  const chip =
    "px-3 py-1 text-sm font-medium rounded-md border transition-colors disabled:opacity-50";
  const on = "border-primary bg-primary text-primary-foreground";
  const off = "border-border text-muted-foreground hover:text-foreground hover:bg-muted/50";
</script>

<section class="slab col-span-full" aria-labelledby="webhook-deliveries">
  <div class="slab-header">
    <h3 id="webhook-deliveries">
      {loading ? "Deliveries" : `Deliveries (${totalCount})`}
    </h3>
  </div>

  <form
    class="slab-body--padded space-y-3 border-b border-[hsl(var(--tn-fg-gutter)/0.3)]"
    onsubmit={(event) => {
      event.preventDefault();
      void load();
    }}
  >
    <div class="flex flex-wrap items-center gap-1" role="group" aria-label="Filter by status">
      <button
        type="button"
        class="{chip} {filter.statuses.length === 0 ? on : off}"
        aria-pressed={filter.statuses.length === 0}
        onclick={() => setStatuses([])}
      >
        All
      </button>
      {#each webhookDeliveryStatuses as status (status.value)}
        <button
          type="button"
          class="{chip} {filter.statuses.includes(status.value) ? on : off}"
          aria-pressed={filter.statuses.includes(status.value)}
          onclick={() => toggleStatus(status.value)}
        >
          {status.label}
        </button>
      {/each}
    </div>
    <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
      <div class="space-y-1">
        <Label for="delivery-event-type" class="text-xs text-muted-foreground">Event type</Label>
        <select
          id="delivery-event-type"
          class="w-full border border-border bg-background px-2 py-1.5 text-sm text-foreground"
          bind:value={filter.eventType}
        >
          <option value="">Any type</option>
          <option value="webhook.test">webhook.test</option>
          {#each specificTypes as type (type)}
            <option value={type}>{type}</option>
          {/each}
        </select>
      </div>
      <div class="space-y-1">
        <Label for="delivery-event-id" class="text-xs text-muted-foreground">Event ID</Label>
        <Input
          id="delivery-event-id"
          bind:value={filter.eventId}
          placeholder="webhook-id header value"
          class="font-mono text-xs"
        />
      </div>
      <div class="space-y-1">
        <Label for="delivery-after" class="text-xs text-muted-foreground">Created after</Label>
        <Input id="delivery-after" type="datetime-local" bind:value={filter.createdAfter} />
      </div>
      <div class="space-y-1">
        <Label for="delivery-before" class="text-xs text-muted-foreground">Created before</Label>
        <Input id="delivery-before" type="datetime-local" bind:value={filter.createdBefore} />
      </div>
    </div>
    <div class="flex gap-2">
      <Button type="submit" size="sm" variant="outline">Apply filters</Button>
      {#if filtered}
        <Button type="button" size="sm" variant="ghost" onclick={clearFilters}>Clear</Button>
      {/if}
    </div>
  </form>

  <div class="slab-body--flush">
    {#if loading && deliveries.length === 0}
      <p role="status" class="p-6 text-sm text-muted-foreground">Loading deliveries…</p>
    {:else if loadError}
      <p role="alert" class="p-6 text-sm text-destructive">{loadError}</p>
    {:else if deliveries.length === 0}
      <EmptyState
        icon="Webhook"
        title={filtered ? "No deliveries match these filters" : "No deliveries yet"}
        description={filtered
          ? "Clear a filter to see other deliveries."
          : "Deliveries appear here when a subscribed event happens. The log keeps 30 days."}
        size="md"
        showAction={false}
      />
    {:else}
      <div class="overflow-x-auto">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Status</TableHead>
              <TableHead>Event</TableHead>
              <TableHead>Last attempt</TableHead>
              <TableHead class="text-right">Attempts</TableHead>
              <TableHead>Created</TableHead>
              <TableHead class="text-right">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {#each deliveries as delivery (delivery.id)}
              <TableRow>
                <TableCell>
                  <Badge variant="outline" class="uppercase {deliveryStatusClass(delivery.status)}">
                    {deliveryStatusLabel(delivery.status)}
                  </Badge>
                </TableCell>
                <TableCell>
                  <div class="font-mono text-xs">{delivery.eventType}</div>
                  <div class="font-mono text-[10px] text-muted-foreground break-all">
                    {delivery.kind === "TEST" ? "Test delivery" : delivery.eventId}
                  </div>
                </TableCell>
                <TableCell class="text-xs">
                  {#if delivery.attempts === 0}
                    <span class="text-muted-foreground">Not attempted</span>
                  {:else}
                    {attemptOutcome(delivery.lastStatusCode, delivery.lastErrorClass)}
                  {/if}
                  {#if delivery.status === "PENDING" && delivery.nextAttemptAt}
                    <div class="text-muted-foreground">
                      Next {formatWebhookTime(delivery.nextAttemptAt)}
                    </div>
                  {/if}
                  {#if delivery.replayCount > 0}
                    <div class="text-muted-foreground">
                      Replayed {delivery.replayCount}×, last {formatWebhookTime(
                        delivery.lastReplayedAt
                      )}
                    </div>
                  {/if}
                </TableCell>
                <TableCell class="text-right">{delivery.attempts}</TableCell>
                <TableCell class="whitespace-nowrap text-xs">
                  {formatWebhookTime(delivery.createdAt)}
                </TableCell>
                <TableCell class="text-right whitespace-nowrap">
                  <Button
                    variant="ghost"
                    size="sm"
                    aria-expanded={expandedId === delivery.id}
                    onclick={() => toggleAttempts(delivery.id)}
                  >
                    {expandedId === delivery.id ? "Hide attempts" : "Attempts"}
                  </Button>
                  {#if replayable(delivery)}
                    <Button
                      variant="ghost"
                      size="sm"
                      disabled={replayingId !== null}
                      onclick={() => replay(delivery.id)}
                    >
                      {replayingId === delivery.id ? "Replaying…" : "Replay"}
                    </Button>
                  {/if}
                </TableCell>
              </TableRow>
              {#if expandedId === delivery.id}
                <TableRow>
                  <TableCell colspan={6} class="bg-muted/20">
                    {#if attemptsLoading}
                      <p role="status" class="text-xs text-muted-foreground">Loading attempts…</p>
                    {:else if attemptsError}
                      <p role="alert" class="text-xs text-destructive">{attemptsError}</p>
                    {:else if attempts.length === 0}
                      <p class="text-xs text-muted-foreground">No attempts yet.</p>
                    {:else}
                      <ol class="space-y-2">
                        {#each attempts as attempt (attempt.id)}
                          <li class="text-xs">
                            <div class="flex flex-wrap gap-x-4">
                              <span class="font-medium">#{attempt.attemptNumber}</span>
                              <span>{attemptOutcome(attempt.statusCode, attempt.errorClass)}</span>
                              <span class="text-muted-foreground">{attempt.latencyMs} ms</span>
                              <span class="text-muted-foreground">
                                {formatWebhookTime(attempt.attemptedAt)}
                              </span>
                            </div>
                            {#if attempt.responseExcerpt}
                              <pre
                                class="mt-1 max-h-32 overflow-auto whitespace-pre-wrap break-all bg-background p-2 font-mono text-[11px] text-muted-foreground">{attempt.responseExcerpt}</pre>
                            {/if}
                          </li>
                        {/each}
                      </ol>
                    {/if}
                  </TableCell>
                </TableRow>
              {/if}
            {/each}
          </TableBody>
        </Table>
      </div>
    {/if}
  </div>
  {#if hasNextPage && !loadError}
    <div class="slab-actions">
      <Button variant="ghost" disabled={loadingMore} onclick={() => load("more")}>
        {loadingMore ? "Loading…" : "Load more"}
      </Button>
    </div>
  {/if}
</section>

<section class="slab col-span-full" aria-labelledby="webhook-range-replay">
  <div class="slab-header">
    <h3 id="webhook-range-replay">Replay a time range</h3>
  </div>
  <form class="slab-body--padded space-y-3" onsubmit={replayRange}>
    <p class="text-sm text-muted-foreground">
      Sends the failed and skipped deliveries created in the range again, oldest first, under their
      original IDs. One run replays at most {RANGE_REPLAY_BATCH.toLocaleString()}; run it again
      while more remain.
      {#if !endpointEnabled}
        Enable the endpoint first.
      {/if}
    </p>
    <div class="grid gap-3 sm:grid-cols-2">
      <div class="space-y-1">
        <Label for="range-after" class="text-xs text-muted-foreground">From</Label>
        <Input id="range-after" type="datetime-local" bind:value={rangeAfter} />
      </div>
      <div class="space-y-1">
        <Label for="range-before" class="text-xs text-muted-foreground">Until</Label>
        <Input id="range-before" type="datetime-local" bind:value={rangeBefore} />
      </div>
    </div>
    <Button type="submit" variant="outline" disabled={rangeReplaying || !endpointEnabled}>
      {rangeReplaying ? "Replaying…" : "Replay failed and skipped"}
    </Button>
    {#if rangeResult}
      <p role="status" class="text-sm text-foreground">{rangeResult}</p>
    {/if}
  </form>
</section>
