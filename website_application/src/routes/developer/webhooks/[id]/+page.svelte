<script lang="ts">
  import { onMount } from "svelte";
  import { goto } from "$app/navigation";
  import { page } from "$app/state";
  import { resolve } from "$app/paths";
  import {
    DeleteWebhookEndpointStore,
    DisableWebhookEndpointStore,
    EnableWebhookEndpointStore,
    GetWebhookEndpointStore,
    GetWebhookEventTypesStore,
    RotateWebhookEndpointSecretStore,
    TestWebhookEndpointStore,
    UpdateWebhookEndpointStore,
  } from "$houdini";
  import { Badge } from "$lib/components/ui/badge";
  import { Button } from "$lib/components/ui/button";
  import { Checkbox } from "$lib/components/ui/checkbox";
  import {
    Dialog,
    DialogContent,
    DialogDescription,
    DialogFooter,
    DialogHeader,
    DialogTitle,
  } from "$lib/components/ui/dialog";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import WebhookDeliveryLog from "$lib/components/webhooks/WebhookDeliveryLog.svelte";
  import WebhookEndpointDialog from "$lib/components/webhooks/WebhookEndpointDialog.svelte";
  import WebhookSecretDialog from "$lib/components/webhooks/WebhookSecretDialog.svelte";
  import { getIconComponent } from "$lib/iconUtils";
  import { toast } from "$lib/stores/toast";
  import {
    ALL_EVENT_TYPES,
    attemptOutcome,
    draftFromEndpoint,
    endpointStatusBadge,
    formatWebhookTime,
    mutationOutcome,
    revealedSecret,
    updateInput,
    type EndpointDraft,
    type RevealedSecret,
    type WebhookAttemptRow,
    type WebhookEndpointRow,
  } from "$lib/webhooks";

  const WebhookIcon = getIconComponent("Webhook");
  const ArrowLeftIcon = getIconComponent("ArrowLeft");

  const endpointId = $derived(page.params.id ?? "");

  const endpointStore = new GetWebhookEndpointStore();
  const eventTypesStore = new GetWebhookEventTypesStore();
  const updateStore = new UpdateWebhookEndpointStore();
  const deleteStore = new DeleteWebhookEndpointStore();
  const enableStore = new EnableWebhookEndpointStore();
  const disableStore = new DisableWebhookEndpointStore();
  const rotateStore = new RotateWebhookEndpointSecretStore();
  const testStore = new TestWebhookEndpointStore();

  type Action = "" | "edit" | "toggle" | "rotate" | "test" | "delete";

  let endpoint = $state.raw<WebhookEndpointRow | null>(null);
  let loaded = $state(false);
  let loadError = $state("");
  let acting = $state<Action>("");
  let editOpen = $state(false);
  let rotateOpen = $state(false);
  let revokePrevious = $state(false);
  // Shown once after a rotation and dropped when the dialog closes.
  let secret = $state<RevealedSecret | null>(null);
  let testAttempt = $state.raw<WebhookAttemptRow | null>(null);
  let deliveryLog = $state<ReturnType<typeof WebhookDeliveryLog> | null>(null);

  const eventTypes = $derived($eventTypesStore.data?.webhookEventTypes ?? []);
  const editDraft = $derived<EndpointDraft>(
    endpoint ? draftFromEndpoint(endpoint) : { url: "", description: "", eventTypes: [] }
  );
  const badge = $derived(endpoint ? endpointStatusBadge(endpoint) : null);

  async function load() {
    loadError = "";
    try {
      const result = await endpointStore.fetch({
        variables: { id: endpointId },
        policy: "NetworkOnly",
      });
      if (result.errors?.length) {
        loadError = result.errors[0].message;
        return;
      }
      endpoint = result.data?.webhookEndpoint ?? null;
    } catch {
      loadError = "Could not load this endpoint.";
    } finally {
      loaded = true;
    }
  }

  onMount(() => {
    void load();
    void eventTypesStore.fetch().catch(() => null);
  });

  async function save(draft: EndpointDraft) {
    if (!endpoint) return;
    const input = updateInput(endpoint, draft);
    if (!input) {
      editOpen = false;
      return;
    }
    acting = "edit";
    try {
      const result = await updateStore.mutate({ id: endpointId, input });
      const outcome = mutationOutcome(
        result.data?.updateWebhookEndpoint,
        result.errors,
        "WebhookEndpoint",
        "The endpoint was not updated."
      );
      if (!outcome.ok) {
        toast.error(outcome.message);
        return;
      }
      editOpen = false;
      toast.success("Endpoint updated");
      await load();
    } catch {
      toast.error("The endpoint was not updated.");
    } finally {
      acting = "";
    }
  }

  async function toggleEnabled() {
    if (!endpoint) return;
    const enabling = endpoint.status !== "ENABLED";
    if (
      !enabling &&
      !confirm("Disable this endpoint? Its pending deliveries are skipped and not sent.")
    ) {
      return;
    }
    acting = "toggle";
    try {
      let data: object | null | undefined;
      let errors: readonly { message: string }[] | null | undefined;
      if (enabling) {
        const result = await enableStore.mutate({ id: endpointId });
        data = result.data?.enableWebhookEndpoint;
        errors = result.errors;
      } else {
        const result = await disableStore.mutate({ id: endpointId });
        data = result.data?.disableWebhookEndpoint;
        errors = result.errors;
      }
      const outcome = mutationOutcome(
        data,
        errors,
        "WebhookEndpoint",
        enabling ? "The endpoint was not enabled." : "The endpoint was not disabled."
      );
      if (!outcome.ok) {
        toast.error(outcome.message);
        return;
      }
      toast.success(
        enabling
          ? "Endpoint enabled. Deliveries skipped while it was disabled are not resent; replay them below."
          : "Endpoint disabled"
      );
      await load();
      deliveryLog?.refresh();
    } catch {
      toast.error(enabling ? "The endpoint was not enabled." : "The endpoint was not disabled.");
    } finally {
      acting = "";
    }
  }

  async function rotate() {
    acting = "rotate";
    try {
      const result = await rotateStore.mutate({ id: endpointId, revokePrevious });
      const outcome = mutationOutcome(
        result.data?.rotateWebhookEndpointSecret,
        result.errors,
        "WebhookEndpointSecret",
        "The secret was not rotated."
      );
      if (!outcome.ok) {
        toast.error(outcome.message);
        return;
      }
      secret = revealedSecret(outcome.value, "rotated");
      rotateOpen = false;
      revokePrevious = false;
      await load();
    } catch {
      toast.error("The secret was not rotated.");
    } finally {
      acting = "";
    }
  }

  async function sendTest() {
    acting = "test";
    testAttempt = null;
    try {
      const result = await testStore.mutate({ id: endpointId });
      const outcome = mutationOutcome(
        result.data?.testWebhookEndpoint,
        result.errors,
        "WebhookTestResult",
        "The test delivery was not sent."
      );
      if (!outcome.ok) {
        toast.error(outcome.message);
        return;
      }
      const value = outcome.value as { attempt?: WebhookAttemptRow };
      testAttempt = value.attempt ?? null;
      deliveryLog?.refresh();
    } catch {
      toast.error("The test delivery was not sent.");
    } finally {
      acting = "";
    }
  }

  async function remove() {
    if (
      !confirm(
        "Delete this endpoint? Its signing secrets and delivery log are deleted, and it stops receiving events."
      )
    ) {
      return;
    }
    acting = "delete";
    try {
      const result = await deleteStore.mutate({ id: endpointId });
      const outcome = mutationOutcome(
        result.data?.deleteWebhookEndpoint,
        result.errors,
        "DeleteSuccess",
        "The endpoint was not deleted."
      );
      if (!outcome.ok) {
        toast.error(outcome.message);
        return;
      }
      toast.success("Endpoint deleted");
      await goto(resolve("/developer/webhooks"));
    } catch {
      toast.error("The endpoint was not deleted.");
    } finally {
      acting = "";
    }
  }
</script>

<svelte:head>
  <title>Webhook Endpoint - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col">
  <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0">
    <a
      href={resolve("/developer/webhooks")}
      class="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
    >
      <ArrowLeftIcon class="w-4 h-4" />
      All endpoints
    </a>
    <div class="mt-2 flex flex-wrap items-center gap-3">
      <WebhookIcon class="w-5 h-5 text-primary" />
      <h1 class="text-xl font-bold text-foreground break-all">
        {endpoint?.url ?? "Webhook endpoint"}
      </h1>
      {#if badge}
        <Badge variant="outline" class="uppercase {badge.className}">{badge.label}</Badge>
      {/if}
    </div>
    {#if endpoint?.description}
      <p class="mt-1 text-sm text-muted-foreground">{endpoint.description}</p>
    {/if}
  </div>

  <div class="flex-1 overflow-y-auto">
    {#if !loaded}
      <p role="status" class="p-6 text-sm text-muted-foreground">Loading endpoint…</p>
    {:else if loadError}
      <p role="alert" class="p-6 text-sm text-destructive">{loadError}</p>
    {:else if !endpoint}
      <EmptyState
        icon="Webhook"
        title="Endpoint not found"
        description="This endpoint does not exist or belongs to another account."
        size="md"
        showAction={false}
      />
    {:else}
      <div class="dashboard-grid">
        <section class="slab col-span-full" aria-labelledby="webhook-overview">
          <div class="slab-header">
            <h3 id="webhook-overview">Endpoint</h3>
          </div>
          <div class="slab-body--padded space-y-4">
            {#if endpoint.status === "DISABLED" && endpoint.disabledReason === "FAILING"}
              <p role="alert" class="text-sm text-destructive">
                Disabled automatically on {formatWebhookTime(endpoint.disabledAt)} after at least 20 failed
                attempts over 5 days without a success. Fix the receiver, enable the endpoint, then replay
                the skipped deliveries.
              </p>
            {/if}
            <dl class="grid gap-x-6 gap-y-3 text-sm sm:grid-cols-2 lg:grid-cols-4">
              <div class="sm:col-span-2 lg:col-span-4">
                <dt class="text-xs text-muted-foreground">Event types</dt>
                <dd class="flex flex-wrap gap-1 pt-1">
                  {#each endpoint.eventTypes as type (type)}
                    <Badge variant="outline" class="font-mono text-xs">
                      {type === ALL_EVENT_TYPES ? "* (every type)" : type}
                    </Badge>
                  {/each}
                </dd>
              </div>
              <div>
                <dt class="text-xs text-muted-foreground">Payload version</dt>
                <dd class="font-mono">{endpoint.apiVersion}</dd>
              </div>
              <div>
                <dt class="text-xs text-muted-foreground">Created</dt>
                <dd>{formatWebhookTime(endpoint.createdAt)}</dd>
              </div>
              <div>
                <dt class="text-xs text-muted-foreground">Last success</dt>
                <dd>{formatWebhookTime(endpoint.lastSuccessAt)}</dd>
              </div>
              <div>
                <dt class="text-xs text-muted-foreground">Last failure</dt>
                <dd>{formatWebhookTime(endpoint.lastFailureAt)}</dd>
              </div>
              <div>
                <dt class="text-xs text-muted-foreground">Consecutive failures</dt>
                <dd class={endpoint.consecutiveFailures > 0 ? "text-destructive" : ""}>
                  {endpoint.consecutiveFailures}
                  {#if endpoint.failingSince}
                    <span class="block text-xs text-muted-foreground">
                      since {formatWebhookTime(endpoint.failingSince)}
                    </span>
                  {/if}
                </dd>
              </div>
              {#if endpoint.previousSecretExpiresAt}
                <div>
                  <dt class="text-xs text-muted-foreground">Previous secret signs until</dt>
                  <dd>{formatWebhookTime(endpoint.previousSecretExpiresAt)}</dd>
                </div>
              {/if}
              {#if endpoint.disabledAt}
                <div>
                  <dt class="text-xs text-muted-foreground">Disabled</dt>
                  <dd>{formatWebhookTime(endpoint.disabledAt)}</dd>
                </div>
              {/if}
            </dl>
            <p class="text-xs text-muted-foreground">
              The endpoint receives events that happen after it was created. Failed deliveries retry
              with backoff for about 3 days.
            </p>
          </div>
          <div class="slab-actions slab-actions--row">
            <Button variant="ghost" disabled={!!acting} onclick={() => (editOpen = true)}>
              Edit
            </Button>
            <Button variant="ghost" disabled={!!acting} onclick={toggleEnabled}>
              {#if acting === "toggle"}
                Saving…
              {:else}
                {endpoint.status === "ENABLED" ? "Disable" : "Enable"}
              {/if}
            </Button>
            <Button variant="ghost" disabled={!!acting} onclick={() => (rotateOpen = true)}>
              Rotate secret
            </Button>
            <Button variant="ghost" disabled={!!acting} onclick={sendTest}>
              {acting === "test" ? "Sending…" : "Send test event"}
            </Button>
            <Button
              variant="ghost"
              class="text-destructive hover:text-destructive"
              disabled={!!acting}
              onclick={remove}
            >
              {acting === "delete" ? "Deleting…" : "Delete"}
            </Button>
          </div>
        </section>

        {#if testAttempt}
          <section class="slab col-span-full" aria-labelledby="webhook-test-result">
            <div class="slab-header">
              <h3 id="webhook-test-result">Test delivery</h3>
            </div>
            <div class="slab-body--padded space-y-2 text-sm">
              <p class={testAttempt.errorClass ? "text-destructive" : "text-success"}>
                {testAttempt.errorClass ? "Failed" : "Succeeded"}:
                {attemptOutcome(testAttempt.statusCode, testAttempt.errorClass)} in {testAttempt.latencyMs}
                ms
              </p>
              {#if testAttempt.responseExcerpt}
                <pre
                  class="max-h-40 overflow-auto whitespace-pre-wrap break-all bg-muted p-2 font-mono text-xs text-muted-foreground">{testAttempt.responseExcerpt}</pre>
              {/if}
            </div>
          </section>
        {/if}

        {#key endpointId}
          <WebhookDeliveryLog
            bind:this={deliveryLog}
            {endpointId}
            endpointEnabled={endpoint.status === "ENABLED"}
            {eventTypes}
          />
        {/key}
      </div>
    {/if}
  </div>
</div>

<WebhookEndpointDialog
  bind:open={editOpen}
  mode="edit"
  initial={editDraft}
  {eventTypes}
  saving={acting === "edit"}
  onSubmit={save}
/>

<Dialog
  open={rotateOpen}
  onOpenChange={(value) => {
    rotateOpen = value;
    if (!value) revokePrevious = false;
  }}
>
  <DialogContent
    class="max-w-lg rounded-none border-[hsl(var(--tn-fg-gutter)/0.3)] bg-background p-0 gap-0 overflow-hidden"
  >
    <DialogHeader class="slab-header text-left space-y-1">
      <DialogTitle class="uppercase tracking-wide text-sm font-semibold text-muted-foreground">
        Rotate Signing Secret
      </DialogTitle>
      <DialogDescription class="text-xs text-muted-foreground/70">
        A new secret is generated and shown once. The previous secret keeps signing alongside it for
        24 hours, so every delivery carries both signatures while your receiver switches over.
      </DialogDescription>
    </DialogHeader>
    <div class="slab-body--padded">
      <label class="flex items-start gap-2 text-sm">
        <Checkbox bind:checked={revokePrevious} disabled={acting === "rotate"} />
        <span>
          Stop signing with the previous secret now
          <span class="block text-xs text-muted-foreground">
            Use this when the previous secret leaked. Receivers that only know it start rejecting
            deliveries immediately.
          </span>
        </span>
      </label>
    </div>
    <DialogFooter class="slab-actions slab-actions--row gap-0">
      <Button
        type="button"
        variant="ghost"
        class="rounded-none h-12 flex-1 border-r border-[hsl(var(--tn-fg-gutter)/0.3)] text-muted-foreground hover:text-foreground"
        disabled={acting === "rotate"}
        onclick={() => (rotateOpen = false)}
      >
        Cancel
      </Button>
      <Button
        type="button"
        variant="ghost"
        class="rounded-none h-12 flex-1 text-primary hover:text-primary/80"
        disabled={acting === "rotate"}
        onclick={rotate}
      >
        {acting === "rotate" ? "Rotating…" : "Rotate secret"}
      </Button>
    </DialogFooter>
  </DialogContent>
</Dialog>

<WebhookSecretDialog {secret} onDismiss={() => (secret = null)} />
