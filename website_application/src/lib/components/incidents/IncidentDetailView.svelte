<script lang="ts">
  import { onMount } from "svelte";
  import { get } from "svelte/store";
  import {
    fragment,
    AcknowledgeIncidentStore,
    AddIncidentNoteStore,
    AssignIncidentStore,
    GetIncidentStore,
    IncidentFieldsStore,
    ResolveIncidentStore,
    type GetIncident$result,
  } from "$houdini";
  import { Badge } from "$lib/components/ui/badge";
  import { Button } from "$lib/components/ui/button";
  import { Textarea } from "$lib/components/ui/textarea";
  import {
    Table,
    TableBody,
    TableCell,
    TableHead,
    TableHeader,
    TableRow,
  } from "$lib/components/ui/table";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import { getIconComponent } from "$lib/iconUtils";
  import { auth } from "$lib/stores/auth";
  import { toast } from "$lib/stores/toast";
  import { refreshFiringIncidentCount, watchIncidentUpdates } from "$lib/stores/incidents.svelte";
  import {
    describeTimelineEvent,
    formatIncidentTime,
    incidentSeverityClass,
    incidentStatusClass,
    incidentStatusLabel,
    labelEntries,
    mutationErrorMessage,
  } from "$lib/incidents";

  let {
    incidentId,
    backHref,
    backLabel,
    reportHref,
    live = false,
    showTenant = false,
  }: {
    incidentId: string;
    backHref: string;
    backLabel: string;
    reportHref?: (reportId: string) => string;
    live?: boolean;
    showTenant?: boolean;
  } = $props();

  const detailStore = new GetIncidentStore();
  const incidentFields = new IncidentFieldsStore();
  const acknowledgeStore = new AcknowledgeIncidentStore();
  const resolveStore = new ResolveIncidentStore();
  const noteStore = new AddIncidentNoteStore();
  const assignStore = new AssignIncidentStore();

  const AlertIcon = getIconComponent("AlertTriangle");
  const ArrowLeftIcon = getIconComponent("ArrowLeft");

  type IncidentAction = "acknowledge" | "resolve" | "note" | "assign" | "unassign";

  type IncidentDetail = NonNullable<GetIncident$result["incident"]>;

  let loaded = $state(false);
  let loadError = $state("");
  let acting = $state<"" | IncidentAction>("");
  let note = $state("");
  let detail = $state.raw<IncidentDetail | null>(null);
  // Set when an incident this view showed stops being visible, e.g. after its
  // cluster moved to another owner.
  let unavailable = $state(false);
  let movedAway = $state(false);
  let request = 0;

  const incident = $derived(detail ? get(fragment(detail.incident, incidentFields)) : null);
  // Incidents are assigned to the person taking them; there is no tenant member
  // picker, and the API only accepts the caller or no assignee.
  const currentUserId = $derived($auth.user?.id ?? "");
  const assignedToMe = $derived(!!incident?.assignedTo && incident.assignedTo === currentUserId);
  const alerts = $derived(detail?.alerts ?? []);
  const timeline = $derived(detail?.timeline ?? []);

  /**
   * A background load keeps the shown incident when the request fails; the next
   * update or reconnect retries it.
   */
  async function load(background = false) {
    const current = ++request;
    if (!background) loadError = "";
    try {
      const result = await detailStore.fetch({
        variables: { id: incidentId },
        policy: "NetworkOnly",
      });
      if (current !== request) return;
      if (result.errors?.length) {
        if (!background || !detail) loadError = result.errors[0].message;
        return;
      }
      const next = result.data?.incident ?? null;
      if (!next && detail) unavailable = true;
      if (next) {
        unavailable = false;
        movedAway = false;
        loadError = "";
      }
      detail = next;
    } catch {
      if (current === request && (!background || !detail)) {
        loadError = "Could not load this incident.";
      }
    } finally {
      if (current === request) loaded = true;
    }
  }

  onMount(() => {
    void load();
    if (!live) return;
    // Every change adds a timeline entry the event does not carry, so a change
    // to this incident always reloads it.
    return watchIncidentUpdates(({ events, reconnected }) => {
      const changes = events.filter((event) => event.incidentId === incidentId);
      if (changes.some((event) => event.change === "scope_changed")) movedAway = true;
      if (reconnected || changes.length > 0) void load(true);
    });
  });

  async function mutate(kind: IncidentAction) {
    if (kind === "assign" || kind === "unassign") {
      const result = await assignStore.mutate({
        id: incidentId,
        assigneeUserId: kind === "assign" ? currentUserId : null,
      });
      return { data: result.data?.assignIncident, errors: result.errors };
    }
    if (kind === "acknowledge") {
      const result = await acknowledgeStore.mutate({ id: incidentId });
      return { data: result.data?.acknowledgeIncident, errors: result.errors };
    }
    if (kind === "resolve") {
      const result = await resolveStore.mutate({ id: incidentId });
      return { data: result.data?.resolveIncident, errors: result.errors };
    }
    const result = await noteStore.mutate({ id: incidentId, body: note.trim() });
    return { data: result.data?.addIncidentNote, errors: result.errors };
  }

  async function runAction(kind: IncidentAction, success: string, fallback: string) {
    acting = kind;
    try {
      const { data, errors } = await mutate(kind);
      const message = mutationErrorMessage(data, errors, fallback);
      if (message) {
        toast.error(message);
        return;
      }
      if (kind === "note") note = "";
      toast.success(success);
      void refreshFiringIncidentCount();
      await load();
    } catch {
      toast.error(fallback);
    } finally {
      acting = "";
    }
  }
</script>

<div class="h-full flex flex-col">
  <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0">
    <!-- Callers pass hrefs built with resolve(). -->
    <!-- eslint-disable svelte/no-navigation-without-resolve -->
    <a
      href={backHref}
      class="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
    >
      <ArrowLeftIcon class="w-4 h-4" />
      {backLabel}
    </a>
    <!-- eslint-enable svelte/no-navigation-without-resolve -->
    <div class="mt-2 flex flex-wrap items-center gap-3">
      <AlertIcon class="w-5 h-5 text-primary" />
      <h1 class="text-xl font-bold text-foreground">{incident?.title ?? "Incident"}</h1>
      {#if incident}
        <Badge variant="outline" class="uppercase {incidentStatusClass(incident.status)}">
          {incidentStatusLabel(incident.status)}
        </Badge>
        <Badge variant="outline" class="uppercase {incidentSeverityClass(incident.severity)}">
          {incident.severity}
        </Badge>
        <Badge variant="outline">
          {incident.scope === "PLATFORM" ? "Platform scope" : "Tenant scope"}
        </Badge>
      {/if}
    </div>
    {#if incident}
      <p class="mt-1 text-sm text-muted-foreground">
        <span class="font-mono">{incident.alertname}</span>
        {#if incident.clusterId}
          <span class="mx-1 text-border">|</span>
          Cluster <span class="font-mono">{incident.clusterId}</span>
        {/if}
        {#if incident.region}
          <span class="mx-1 text-border">|</span>
          {incident.region}
        {/if}
        {#if showTenant && incident.tenantId}
          <span class="mx-1 text-border">|</span>
          Tenant <span class="font-mono">{incident.tenantId}</span>
        {/if}
      </p>
    {/if}
  </div>

  <div class="flex-1 overflow-y-auto">
    {#if !loaded}
      <p role="status" class="p-6 text-sm text-muted-foreground">Loading incident…</p>
    {:else if loadError}
      <p role="alert" class="p-6 text-sm text-destructive">{loadError}</p>
    {:else if unavailable}
      <EmptyState
        icon="AlertTriangle"
        title="Incident no longer available"
        description={movedAway
          ? "Ownership of this incident's cluster changed, so the incident is no longer visible to your account."
          : "This incident is no longer visible to your account."}
        size="md"
        showAction={false}
      />
    {:else if !incident}
      <EmptyState
        icon="AlertTriangle"
        title="Incident not found"
        description="This incident does not exist or is not visible to your account."
        size="md"
        showAction={false}
      />
    {:else}
      <div class="dashboard-grid">
        <section class="slab col-span-full" aria-labelledby="incident-overview">
          <div class="slab-header">
            <h3 id="incident-overview">Overview</h3>
          </div>
          <div class="slab-body--padded space-y-4">
            {#if incident.summary}
              <p class="text-sm text-foreground">{incident.summary}</p>
            {/if}
            <dl class="grid gap-x-6 gap-y-3 text-sm sm:grid-cols-2 lg:grid-cols-4">
              <div>
                <dt class="text-xs text-muted-foreground">Started</dt>
                <dd>{formatIncidentTime(incident.startedAt)}</dd>
              </div>
              <div>
                <dt class="text-xs text-muted-foreground">Last alert</dt>
                <dd>{formatIncidentTime(incident.lastAlertAt)}</dd>
              </div>
              <div>
                <dt class="text-xs text-muted-foreground">Firing alerts</dt>
                <dd>{incident.firingAlertCount}</dd>
              </div>
              <div>
                <dt class="text-xs text-muted-foreground">Assigned to</dt>
                <dd class={assignedToMe || !incident.assignedTo ? "" : "font-mono text-xs"}>
                  {assignedToMe ? "You" : (incident.assignedTo ?? "Nobody")}
                </dd>
              </div>
              {#if incident.acknowledgedAt}
                <div>
                  <dt class="text-xs text-muted-foreground">Acknowledged</dt>
                  <dd>
                    {formatIncidentTime(incident.acknowledgedAt)}
                    {#if incident.acknowledgedBy}
                      <span class="block font-mono text-xs text-muted-foreground">
                        {incident.acknowledgedBy}
                      </span>
                    {/if}
                  </dd>
                </div>
              {/if}
              {#if incident.resolvedAt}
                <div>
                  <dt class="text-xs text-muted-foreground">Resolved</dt>
                  <dd>
                    {formatIncidentTime(incident.resolvedAt)}
                    {incident.resolution === "AUTO" ? "(alerts cleared)" : "(manually)"}
                    {#if incident.resolvedBy}
                      <span class="block font-mono text-xs text-muted-foreground">
                        {incident.resolvedBy}
                      </span>
                    {/if}
                  </dd>
                </div>
              {/if}
            </dl>
            {#if incident.status !== "RESOLVED"}
              <p class="text-xs text-muted-foreground">
                Acknowledging keeps the incident open until its alerts clear. Resolving closes it;
                repeats of the same alerts do not reopen it, and a new alert opens a new incident.
              </p>
            {/if}
          </div>
          {#if incident.status !== "RESOLVED"}
            <div class="slab-actions slab-actions--row">
              {#if incident.status === "FIRING"}
                <Button
                  variant="ghost"
                  disabled={!!acting}
                  onclick={() =>
                    runAction(
                      "acknowledge",
                      "Incident acknowledged",
                      "The incident was not acknowledged."
                    )}
                >
                  {acting === "acknowledge" ? "Acknowledging…" : "Acknowledge"}
                </Button>
              {/if}
              <Button
                variant="ghost"
                disabled={!!acting}
                onclick={() =>
                  runAction("resolve", "Incident resolved", "The incident was not resolved.")}
              >
                {acting === "resolve" ? "Resolving…" : "Resolve"}
              </Button>
              {#if assignedToMe}
                <Button
                  variant="ghost"
                  disabled={!!acting}
                  onclick={() =>
                    runAction(
                      "unassign",
                      "Incident unassigned",
                      "The incident was not unassigned."
                    )}
                >
                  {acting === "unassign" ? "Unassigning…" : "Unassign me"}
                </Button>
              {:else if currentUserId}
                <Button
                  variant="ghost"
                  disabled={!!acting}
                  onclick={() =>
                    runAction(
                      "assign",
                      "Incident assigned to you",
                      "The incident was not assigned."
                    )}
                >
                  {acting === "assign" ? "Assigning…" : "Assign to me"}
                </Button>
              {/if}
            </div>
          {/if}
        </section>

        <section class="slab col-span-full" aria-labelledby="incident-alerts">
          <div class="slab-header">
            <h3 id="incident-alerts">Alerts ({alerts.length})</h3>
          </div>
          <div class="slab-body--flush">
            {#if alerts.length === 0}
              <p class="p-4 text-sm text-muted-foreground">No alerts are recorded.</p>
            {:else}
              <div class="overflow-x-auto">
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>Status</TableHead>
                      <TableHead>Alert</TableHead>
                      <TableHead>Started</TableHead>
                      <TableHead>Ended</TableHead>
                      <TableHead>Details</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {#each alerts as alert (alert.fingerprint)}
                      {@const labels = labelEntries(alert.labels)}
                      {@const annotations = labelEntries(alert.annotations)}
                      <TableRow>
                        <TableCell>
                          <Badge
                            variant="outline"
                            class="uppercase {alert.status === 'firing'
                              ? 'border-destructive/60 text-destructive'
                              : 'border-success/60 text-success'}"
                          >
                            {alert.status}
                          </Badge>
                        </TableCell>
                        <TableCell>
                          <div class="font-mono text-xs">
                            {String(alert.labels.alertname ?? incident.alertname)}
                          </div>
                          {#if alert.annotations.summary}
                            <div class="text-xs text-muted-foreground">
                              {String(alert.annotations.summary)}
                            </div>
                          {/if}
                        </TableCell>
                        <TableCell class="whitespace-nowrap">
                          {formatIncidentTime(alert.startsAt)}
                        </TableCell>
                        <TableCell class="whitespace-nowrap">
                          {alert.endsAt ? formatIncidentTime(alert.endsAt) : "—"}
                        </TableCell>
                        <TableCell class="min-w-[16rem]">
                          <details>
                            <summary class="cursor-pointer text-xs text-muted-foreground">
                              {labels.length} labels · {annotations.length} annotations
                            </summary>
                            <dl class="mt-2 space-y-1 text-xs">
                              {#each labels as [key, value] (`label-${key}`)}
                                <div class="flex gap-2">
                                  <dt class="font-mono text-muted-foreground">{key}</dt>
                                  <dd class="font-mono break-all">{value}</dd>
                                </div>
                              {/each}
                              {#each annotations as [key, value] (`annotation-${key}`)}
                                <div class="flex gap-2">
                                  <dt class="text-muted-foreground">{key}</dt>
                                  <dd class="break-words">{value}</dd>
                                </div>
                              {/each}
                            </dl>
                            <p class="mt-2 font-mono text-[10px] text-muted-foreground break-all">
                              Fingerprint {alert.fingerprint}
                            </p>
                          </details>
                        </TableCell>
                      </TableRow>
                    {/each}
                  </TableBody>
                </Table>
              </div>
            {/if}
          </div>
        </section>

        <section class="slab col-span-full" aria-labelledby="incident-timeline">
          <div class="slab-header">
            <h3 id="incident-timeline">Timeline</h3>
          </div>
          <div class="slab-body--flush">
            {#if timeline.length === 0}
              <p class="p-4 text-sm text-muted-foreground">No timeline events yet.</p>
            {:else}
              <ol class="divide-y divide-[hsl(var(--tn-fg-gutter)/0.3)]">
                {#each timeline as event (event.id)}
                  <li class="px-4 py-3 text-sm">
                    <div class="flex flex-wrap items-baseline justify-between gap-2">
                      <span class="font-medium text-foreground">{describeTimelineEvent(event)}</span
                      >
                      <span class="text-xs text-muted-foreground whitespace-nowrap">
                        {formatIncidentTime(event.createdAt)}
                      </span>
                    </div>
                    {#if event.kind === "NOTE" && event.note}
                      <p class="mt-1 whitespace-pre-wrap text-foreground">{event.note}</p>
                    {/if}
                    {#if event.kind === "INVESTIGATION_ATTACHED" && event.reportId}
                      {#if reportHref}
                        <!-- eslint-disable svelte/no-navigation-without-resolve -->
                        <a
                          class="mt-1 inline-block text-primary hover:underline"
                          href={reportHref(event.reportId)}>Open the investigation report</a
                        >
                        <!-- eslint-enable svelte/no-navigation-without-resolve -->
                      {:else}
                        <p class="mt-1 font-mono text-xs text-muted-foreground">
                          Report {event.reportId}
                        </p>
                      {/if}
                    {/if}
                    {#if event.actorUserId}
                      <p class="mt-1 font-mono text-xs text-muted-foreground">
                        By {event.actorUserId}
                      </p>
                    {/if}
                  </li>
                {/each}
              </ol>
            {/if}
          </div>
          <form
            class="slab-body--padded space-y-2 border-t border-[hsl(var(--tn-fg-gutter)/0.3)]"
            onsubmit={(event) => {
              event.preventDefault();
              if (note.trim()) void runAction("note", "Note added", "The note was not added.");
            }}
          >
            <label for="incident-note" class="block text-sm font-medium text-foreground">
              Add a note
            </label>
            <Textarea
              id="incident-note"
              bind:value={note}
              rows={3}
              placeholder="What you checked or changed"
              disabled={!!acting}
            />
            <Button type="submit" variant="outline" disabled={!!acting || !note.trim()}>
              {acting === "note" ? "Adding…" : "Add note"}
            </Button>
          </form>
        </section>
      </div>
    {/if}
  </div>
</div>
