<script lang="ts">
  import { onMount } from "svelte";
  import { resolve } from "$app/paths";
  import {
    CreateWebhookEndpointStore,
    GetWebhookEndpointsConnectionStore,
    GetWebhookEventTypesStore,
  } from "$houdini";
  import { Badge } from "$lib/components/ui/badge";
  import { Button } from "$lib/components/ui/button";
  import {
    Table,
    TableBody,
    TableCell,
    TableHead,
    TableHeader,
    TableRow,
  } from "$lib/components/ui/table";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import WebhookEndpointDialog from "$lib/components/webhooks/WebhookEndpointDialog.svelte";
  import WebhookSecretDialog from "$lib/components/webhooks/WebhookSecretDialog.svelte";
  import { getIconComponent } from "$lib/iconUtils";
  import { getDocsSiteUrl } from "$lib/config";
  import { toast } from "$lib/stores/toast";
  import {
    MAX_WEBHOOK_ENDPOINTS,
    createInput,
    endpointHealthSummary,
    endpointStatusBadge,
    eventTypesSummary,
    formatWebhookTime,
    mutationOutcome,
    revealedSecret,
    type EndpointDraft,
    type RevealedSecret,
    type WebhookEndpointRow,
  } from "$lib/webhooks";

  const receiveWebhooksDocsUrl = `${getDocsSiteUrl().replace(/\/$/, "")}/builders/sdks#receive-webhooks`;

  const WebhookIcon = getIconComponent("Webhook");
  const PlusIcon = getIconComponent("Plus");
  const ArrowRightIcon = getIconComponent("ArrowRight");

  const endpointsStore = new GetWebhookEndpointsConnectionStore();
  const eventTypesStore = new GetWebhookEventTypesStore();
  const createStore = new CreateWebhookEndpointStore();

  const emptyDraft: EndpointDraft = { url: "", description: "", eventTypes: [] };

  let endpoints = $state.raw<WebhookEndpointRow[]>([]);
  let totalCount = $state(0);
  let loading = $state(true);
  let loadError = $state("");
  let createOpen = $state(false);
  let creating = $state(false);
  // The one-time secret lives only in this component state; it is never
  // refetched and is dropped when the dialog closes or the page unmounts.
  let secret = $state<RevealedSecret | null>(null);

  const eventTypes = $derived($eventTypesStore.data?.webhookEventTypes ?? []);
  const atLimit = $derived(totalCount >= MAX_WEBHOOK_ENDPOINTS);

  async function load() {
    loadError = "";
    try {
      const result = await endpointsStore.fetch({ policy: "NetworkOnly" });
      const connection = result.data?.webhookEndpointsConnection;
      if (!connection) {
        loadError = result.errors?.[0]?.message ?? "Could not load webhook endpoints.";
        return;
      }
      endpoints = connection.edges.map((edge) => edge.node);
      totalCount = connection.totalCount;
    } catch {
      loadError = "Could not load webhook endpoints.";
    } finally {
      loading = false;
    }
  }

  onMount(() => {
    void load();
    void eventTypesStore.fetch().catch(() => null);
  });

  async function create(draft: EndpointDraft) {
    creating = true;
    try {
      const result = await createStore.mutate({ input: createInput(draft) });
      const outcome = mutationOutcome(
        result.data?.createWebhookEndpoint,
        result.errors,
        "WebhookEndpointSecret",
        "The endpoint was not created."
      );
      if (!outcome.ok) {
        toast.error(outcome.message);
        return;
      }
      secret = revealedSecret(outcome.value, "created");
      createOpen = false;
      toast.success("Webhook endpoint added");
      await load();
    } catch {
      toast.error("The endpoint was not created.");
    } finally {
      creating = false;
    }
  }
</script>

<svelte:head>
  <title>Webhooks - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col">
  <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <div class="flex items-center gap-3">
        <WebhookIcon class="w-5 h-5 text-primary" />
        <div>
          <h1 class="text-xl font-bold text-foreground">Webhooks</h1>
          <p class="text-sm text-muted-foreground">
            Signed HTTPS deliveries of your account's public events, with retries, a 30-day log, and
            replay
          </p>
          <p class="text-xs text-muted-foreground">
            The SDKs verify delivery signatures and parse typed events.
            <!-- eslint-disable svelte/no-navigation-without-resolve -->
            <a
              href={receiveWebhooksDocsUrl}
              target="_blank"
              rel="noopener noreferrer"
              class="text-primary hover:underline">Receive webhooks</a
            >
            <!-- eslint-enable svelte/no-navigation-without-resolve -->
            ·
            <a href={resolve("/developer/sdks")} class="text-primary hover:underline">SDKs</a>
          </p>
        </div>
      </div>
      <Button
        size="sm"
        class="gap-1.5"
        disabled={loading || atLimit}
        title={atLimit ? `An account can have at most ${MAX_WEBHOOK_ENDPOINTS} endpoints.` : ""}
        onclick={() => (createOpen = true)}
      >
        <PlusIcon class="w-3.5 h-3.5" />
        Add endpoint
      </Button>
    </div>
  </div>

  <div class="flex-1 overflow-y-auto">
    <div class="slab">
      <div class="slab-header">
        <h3>
          {loading ? "Endpoints" : `Endpoints (${totalCount} of ${MAX_WEBHOOK_ENDPOINTS})`}
        </h3>
      </div>
      <div class="slab-body--flush">
        {#if loading}
          <p role="status" class="p-6 text-sm text-muted-foreground">Loading endpoints…</p>
        {:else if loadError}
          <p role="alert" class="p-6 text-sm text-destructive">{loadError}</p>
        {:else if endpoints.length === 0}
          <EmptyState
            icon="Webhook"
            title="No webhook endpoints"
            description="Add an https endpoint to receive stream, media, billing, and account events as signed POST requests. An endpoint receives events that happen after it is created."
            actionText="Add endpoint"
            onAction={() => (createOpen = true)}
            size="md"
          />
        {:else}
          <div class="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Status</TableHead>
                  <TableHead>Endpoint</TableHead>
                  <TableHead>Events</TableHead>
                  <TableHead>Delivery health</TableHead>
                  <TableHead>Created</TableHead>
                  <TableHead class="text-right">Action</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {#each endpoints as endpoint (endpoint.id)}
                  {@const badge = endpointStatusBadge(endpoint)}
                  <TableRow>
                    <TableCell>
                      <Badge variant="outline" class="uppercase {badge.className}">
                        {badge.label}
                      </Badge>
                    </TableCell>
                    <TableCell class="max-w-[24rem]">
                      <div class="font-mono text-xs break-all">{endpoint.url}</div>
                      {#if endpoint.description}
                        <div class="text-xs text-muted-foreground">{endpoint.description}</div>
                      {/if}
                    </TableCell>
                    <TableCell class="text-xs">{eventTypesSummary(endpoint.eventTypes)}</TableCell>
                    <TableCell
                      class="text-xs {endpoint.consecutiveFailures > 0
                        ? 'text-destructive'
                        : 'text-muted-foreground'}"
                    >
                      {endpointHealthSummary(endpoint)}
                    </TableCell>
                    <TableCell class="whitespace-nowrap text-xs">
                      {formatWebhookTime(endpoint.createdAt)}
                    </TableCell>
                    <TableCell class="text-right">
                      <Button
                        variant="ghost"
                        size="sm"
                        class="gap-1"
                        href={resolve("/developer/webhooks/[id]", { id: endpoint.id })}
                      >
                        Open
                        <ArrowRightIcon class="w-3.5 h-3.5" />
                      </Button>
                    </TableCell>
                  </TableRow>
                {/each}
              </TableBody>
            </Table>
          </div>
        {/if}
      </div>
    </div>
  </div>
</div>

<WebhookEndpointDialog
  bind:open={createOpen}
  mode="create"
  initial={emptyDraft}
  {eventTypes}
  saving={creating}
  onSubmit={create}
/>

<WebhookSecretDialog {secret} onDismiss={() => (secret = null)} />
