<script lang="ts">
  import { onMount, onDestroy, untrack } from "svelte";
  import { beforeNavigate } from "$app/navigation";
  import { Button } from "$lib/components/ui/button";
  import { auth } from "$lib/stores/auth";
  import { consentAPI } from "$lib/placement/consent-api";
  import {
    ConsentSession,
    consentDirty,
    type ConsentDraft,
    type ConsentState,
  } from "$lib/placement/consent-session";
  import PlacementRollout from "./PlacementRollout.svelte";

  let { clusterId }: { clusterId: string } = $props();
  const session = new ConsentSession(consentAPI, (value) => (view = value));
  let view: ConsentState = $state.raw(session.state);
  let clock = $state(Date.now());
  const identity = $derived(
    $auth.isAuthenticated && $auth.user?.tenant_id
      ? `${$auth.user.tenant_id}:${$auth.user.id}:${$auth.user.role ?? ""}`
      : ""
  );
  const dirty = $derived(consentDirty(view));
  const locked = $derived(!!view.pending || view.phase === "loading" || view.readOnly);
  const reviewExpired = $derived(
    !!view.review &&
      (!Number.isFinite(Date.parse(view.review.expiresAt)) ||
        Date.parse(view.review.expiresAt) <= clock)
  );
  const canApply = $derived(
    !!view.review &&
      dirty &&
      !locked &&
      !view.conflict &&
      !reviewExpired &&
      view.review.warnings.every(
        (warning) => !warning.acknowledgementRequired || view.acknowledgements.includes(warning.id)
      )
  );
  const permissions: { key: keyof ConsentDraft; label: string; description: string }[] = [
    {
      key: "allowIngest",
      label: "Accept publishers",
      description: "Allow new ingest placement on this cluster.",
    },
    {
      key: "allowServe",
      label: "Serve viewers",
      description: "Allow new viewer placement on this cluster.",
    },
    {
      key: "allowExternalSource",
      label: "Pull media from other clusters",
      description:
        "Allow this cluster to receive media from an external source cluster. Same-cluster sources do not need this permission.",
    },
  ];

  $effect(() => {
    const currentIdentity = identity,
      currentCluster = clusterId;
    untrack(() => {
      void session.bind(currentIdentity, currentCluster);
    });
  });
  beforeNavigate((navigation) => {
    if (!dirty && !view.pending) return;
    if (
      navigation.willUnload ||
      !window.confirm(
        view.pending
          ? "This save is not yet confirmed. Leave this page and lose its in-memory recovery request?"
          : "Leave and discard your unsaved capacity permissions?"
      )
    )
      navigation.cancel();
  });
  onMount(() => {
    const timer = setInterval(() => (clock = Date.now()), 1000);
    let stopped = false,
      delay = 2000;
    let polling: ReturnType<typeof setTimeout>;
    async function poll() {
      if (stopped) return;
      if (
        !document.hidden &&
        !dirty &&
        !view.pending &&
        view.phase === "idle" &&
        view.consent?.rollout.status === "PENDING"
      ) {
        await session.load();
        delay = Math.min(delay * 2, 30000);
      }
      if (!stopped) polling = setTimeout(poll, delay);
    }
    polling = setTimeout(poll, delay);
    return () => {
      stopped = true;
      clearInterval(timer);
      clearTimeout(polling);
    };
  });
  onDestroy(() => session.dispose());
</script>

<section class="slab col-span-full capacity-consent" aria-label="Cluster capacity permissions">
  <div class="slab-header"><h3>Capacity permissions</h3></div>
  <div class="slab-body--padded space-y-5">
    <p class="text-sm text-muted-foreground">
      As the cluster owner, choose what this capacity may accept. Tenant and stream placement rules
      can further restrict these permissions; they cannot override them. Enabling a permission does
      not grant cluster access or change subscriptions, prices, or publishing credentials.
    </p>
    {#if !identity}
      <p>Sign in to view capacity permissions.</p>
    {:else if !view.consent && view.phase === "loading"}
      <p role="status">Loading capacity permissions…</p>
    {/if}
    {#if view.error}<p role="alert" class="text-sm text-destructive">{view.error}</p>{/if}
    {#if view.consent && view.draft}
      {#if view.readOnly}<p class="text-sm">
          Read-only. Managing capacity permissions requires cluster-owner authorization.
        </p>{/if}
      <fieldset disabled={locked} class="space-y-4">
        <legend class="sr-only">Allowed media operations</legend>
        {#each permissions as permission (permission.key)}
          <label class="flex items-start gap-3">
            <input
              type="checkbox"
              class="mt-1 accent-primary"
              checked={view.draft[permission.key]}
              onchange={(event) => session.edit(permission.key, event.currentTarget.checked)}
            />
            <span
              ><span class="block text-sm font-medium">{permission.label}</span>
              <span class="block text-sm text-muted-foreground">{permission.description}</span
              ></span
            >
          </label>
        {/each}
      </fieldset>
      <PlacementRollout rollout={view.consent.rollout} revision={view.consent.revision} />
    {/if}
    {#if view.conflict}
      <div class="space-y-2 text-sm">
        <p>
          These permissions changed elsewhere. Reload the current revision, then review your draft
          again.
        </p>
        <Button variant="outline" disabled={locked} onclick={() => session.load(true)}
          >Reload and keep draft</Button
        >
        <Button variant="outline" disabled={locked} onclick={() => session.load()}
          >Reload saved permissions</Button
        >
      </div>
    {/if}
    {#if view.review}
      <section class="space-y-3 text-sm" aria-label="Capacity permission review">
        <h4 class="font-medium">Review capacity changes</h4>
        <dl class="space-y-2">
          {#each view.review.differences as difference, index (index)}
            <div>
              <dt class="font-medium">{difference.label}</dt>
              <dd class="text-muted-foreground break-words">
                {difference.before} → {difference.after}
              </dd>
            </div>
          {/each}
        </dl>
        <p>
          {view.review.impact.affectedStreams} affected streams; {view.review.impact
            .activePublishers} active publishers observed.
          {#if !view.review.impact.complete}Impact coverage is incomplete; these counts are not a
            full inventory.{/if}
        </p>
        {#if view.review.impact.existingSessionsRetained}<p>
            Already admitted sessions retain their admission. Changes govern new placement
            decisions.
          </p>{/if}
        {#each view.review.warnings as warning (warning.id)}
          {#if warning.acknowledgementRequired}
            <label class="flex items-start gap-3 text-warning">
              <input
                type="checkbox"
                class="mt-1 accent-primary"
                disabled={locked}
                checked={view.acknowledgements.includes(warning.id)}
                onchange={(event) => session.acknowledge(warning.id, event.currentTarget.checked)}
              />
              <span>{warning.message}</span>
            </label>
          {:else}<p class="text-warning">{warning.message}</p>{/if}
        {/each}
        <p class="text-muted-foreground">
          {reviewExpired
            ? "Review expired. Review again before applying."
            : `Review expires ${view.review.expiresAt}.`}
        </p>
      </section>
    {/if}
    {#if view.pending}
      <p class="text-sm break-all">
        Recovery request: <code>{view.pending.idempotencyKey}</code>. Keep this page open until its
        outcome is confirmed.
      </p>
    {:else if view.change}
      <p role="status" class="text-sm">
        Capacity revision {view.change.revision} saved. Saving is not confirmation that every required
        recipient has applied it.
      </p>
    {/if}
  </div>
  {#if identity}
    <div class="slab-actions slab-actions--row flex-wrap">
      {#if view.pending}
        <Button
          variant="ghost"
          disabled={view.phase !== "uncertain"}
          onclick={() => session.recover()}>Check save status</Button
        >
        <Button
          variant="ghost"
          disabled={view.phase !== "uncertain"}
          onclick={() => session.recover(true)}>Retry same change</Button
        >
      {:else}
        <Button
          variant="ghost"
          disabled={view.phase !== "idle" || dirty}
          onclick={() => session.load()}>Refresh status</Button
        >
        {#if view.consent?.canManage && !view.readOnly}
          <Button
            variant="ghost"
            disabled={locked || !dirty || view.conflict}
            onclick={() => session.discard()}>Discard draft</Button
          >
          <Button
            variant="ghost"
            disabled={locked || !dirty || view.conflict || view.phase === "reviewing"}
            onclick={() => session.review()}
          >
            {view.phase === "reviewing" ? "Reviewing…" : "Review changes"}
          </Button>
          <Button variant="ghost" disabled={!canApply} onclick={() => session.apply()}
            >Apply permissions</Button
          >
        {/if}
      {/if}
    </div>
  {/if}
</section>

<style>
  .capacity-consent label,
  .capacity-consent :global(button) {
    min-height: 44px;
  }
  .capacity-consent :global(.slab-actions button) {
    min-width: 140px;
    white-space: normal;
  }
</style>
