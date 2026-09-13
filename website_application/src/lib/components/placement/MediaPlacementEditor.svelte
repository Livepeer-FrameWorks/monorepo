<script lang="ts">
  import { onMount, onDestroy, untrack } from "svelte";
  import { beforeNavigate } from "$app/navigation";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { auth } from "$lib/stores/auth";
  import { resolveOperationalStreamId } from "$lib/route-ids";
  import { placementAPI } from "$lib/placement/api";
  import { PlacementSession, type EditorState } from "$lib/placement/session";
  import {
    rolloutNeedsRefresh,
    selectorLabel,
    updatesFor,
    type Scope,
    type Verb,
  } from "$lib/placement/model";
  import PlacementRulesEditor from "./PlacementRulesEditor.svelte";
  import PlacementRollout from "./PlacementRollout.svelte";

  let { scope, initialVerb = "SERVE" }: { scope: Scope; initialVerb?: Verb } = $props();
  const session: PlacementSession = new PlacementSession(placementAPI, (value) => (view = value));
  let view: EditorState = $state.raw(session.state);
  let verb = $state<Verb>("SERVE");
  let latitude = $state("");
  let longitude = $state("");
  let protocol = $state("");
  let previewStreamId = $state("");
  let previewError = $state("");
  let clock = $state(Date.now());
  let discardPending = $state(false);
  let recoveryKeyCopied = $state(false);
  // Role is part of the bind identity so a permission change re-reads the policy.
  // It is deliberately not part of the account identity: an unresolved apply is
  // the same operator's to resolve whether or not their role changed underneath
  // them, and keying its recovery handle on a mutable attribute would silently
  // strand it.
  const account = $derived(
    $auth.isAuthenticated && $auth.user?.tenant_id ? `${$auth.user.tenant_id}:${$auth.user.id}` : ""
  );
  const identity = $derived(account ? `${account}:${$auth.user?.role ?? ""}` : "");
  const scopeKey = $derived(`${identity}:${scope.kind}:${scope.streamId ?? ""}`);
  const dirty = $derived(!!view.policy && updatesFor(view.policy, view.drafts).length > 0);
  const locked = $derived(!!view.pending || view.phase === "loading" || view.readOnly);
  const verbPolicy = $derived(view.policy?.verbs.find((item) => item.verb === verb));
  const reviewExpired = $derived(!!view.review && Date.parse(view.review.expiresAt) <= clock);
  const previewExpired = $derived(!!view.preview && Date.parse(view.preview.expiresAt) <= clock);
  const requiredAcknowledgements = $derived(
    view.review?.warnings.filter((warning) => warning.acknowledgementRequired) ?? []
  );
  const canApply = $derived(
    !!view.review &&
      !reviewExpired &&
      !locked &&
      !view.conflict &&
      requiredAcknowledgements.every((warning) => view.acknowledgements.includes(warning.id))
  );

  $effect(() => {
    const currentIdentity = identity;
    const currentAccount = account;
    const currentScope = { ...scope };
    const startingVerb = initialVerb;
    untrack(() => {
      verb = startingVerb;
      latitude = "";
      longitude = "";
      protocol = "";
      previewStreamId = "";
      discardPending = false;
      previewError = "";
      void session.bind(currentIdentity, currentScope, currentAccount);
    });
  });

  beforeNavigate((navigation) => {
    if (!dirty && !view.pending) return;
    if (navigation.willUnload) {
      navigation.cancel();
      return;
    }
    if (
      !window.confirm(
        view.pending
          ? view.pendingPersisted
            ? "This save is not yet confirmed. Leave this page? The recovery request is kept, and the change stays unresolved until you check it."
            : "This save is not yet confirmed, and this browser is not storing its recovery key. Leave this page and lose it?"
          : "Leave and discard your unsaved placement draft?"
      )
    )
      navigation.cancel();
  });

  onMount(() => {
    const timer = setInterval(() => (clock = Date.now()), 1000);
    let delay = 2000;
    let stopped = false;
    let polling: ReturnType<typeof setTimeout>;
    async function poll() {
      if (stopped) return;
      if (
        !document.hidden &&
        !dirty &&
        !view.pending &&
        view.phase === "idle" &&
        view.policy &&
        rolloutNeedsRefresh(view.policy)
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

  function changePreviewInput() {
    session.invalidatePreview();
    previewError = "";
  }
  function switchVerb(next: Verb) {
    verb = next;
    protocol = "";
    changePreviewInput();
  }

  async function preview() {
    previewError = "";
    const hasLocation = latitude.trim() !== "" || longitude.trim() !== "";
    const lat = Number(latitude),
      lon = Number(longitude);
    if (
      hasLocation &&
      (!latitude.trim() ||
        !longitude.trim() ||
        !Number.isFinite(lat) ||
        !Number.isFinite(lon) ||
        Math.abs(lat) > 90 ||
        Math.abs(lon) > 180)
    ) {
      previewError =
        "Enter both coordinates: latitude −90 to 90, longitude −180 to 180, or leave both blank for unknown location.";
      return;
    }
    const enteredStreamId = previewStreamId.trim();
    const streamId =
      scope.kind === "STREAM"
        ? scope.streamId
        : enteredStreamId
          ? resolveOperationalStreamId({ routeParamId: enteredStreamId })
          : null;
    if (scope.kind === "TENANT" && enteredStreamId && !streamId) {
      previewError =
        "Enter a stream UUID or its Stream ID from the app, or leave it blank for a capacity-only preview.";
      return;
    }
    await session.preview({
      verb,
      protocol: protocol || null,
      streamId,
      coordinates: hasLocation ? { latitude: lat, longitude: lon } : null,
    });
  }
</script>

<div
  class="slab [&_button]:min-h-11 [&_button]:min-w-11 [&_button]:whitespace-normal [&_select]:min-h-11 [&_input:not([type=checkbox])]:min-h-11 [&_summary]:min-h-11 [&_summary]:py-2 [&_label]:min-h-11"
>
  <div class="slab-header">
    <h2>{scope.kind === "STREAM" ? "Stream" : "Account"} media placement</h2>
  </div>
  <div class="slab-body--padded space-y-4">
    <p class="text-sm text-muted-foreground">
      Choose where media enters and reaches viewers. These rules do not change storage or processing
      placement.
    </p>
    {#if !identity}<p role="status">Sign in to a tenant account to manage placement.</p>
    {:else if !view.policy && view.phase === "loading"}<p role="status">Loading placement rules…</p>
    {/if}
    {#if view.change}
      <p role="status" class="text-sm">
        Change saved as revision {view.change.revision}. {view.change.rollout.status === "EFFECTIVE"
          ? "Effective for new decisions."
          : "Saving alone does not confirm enforcement."}
      </p>
    {/if}
    {#if view.policy}
      <PlacementRollout
        rollout={view.policy.rollout}
        revision={view.policy.revision}
        activeRevision={view.policy.activeRevision}
        parentRevision={scope.kind === "STREAM" ? view.policy.parentRevision : undefined}
        activeParentRevision={view.policy.activeParentRevision}
      />
      {#if view.readOnly}<p class="text-sm">
          You can inspect these rules, but your current permissions do not allow changes.
        </p>{/if}
    {/if}
    {#if view.error}<p role="alert" class="text-sm text-destructive">{view.error}</p>{/if}
    {#if view.conflict}
      <p class="text-sm">
        Another update changed the base rules. Reload the current revision before reviewing; your
        draft can be retained for comparison.
      </p>
      <!-- Reloading is a read, so it stays available to an operator who cannot
           edit. Gating it on `locked` meant a conflict plus an expired token
           disabled the only control that could clear either. -->
      <Button
        variant="outline"
        disabled={!!view.pending || view.phase === "loading"}
        onclick={() => session.load(true)}>Load current revision, retain draft</Button
      >
    {:else if !view.pending}
      <Button
        variant="outline"
        size="sm"
        disabled={view.phase !== "idle"}
        onclick={() => session.load(dirty)}>Refresh status{dirty ? " (keep draft)" : ""}</Button
      >
    {/if}
    {#if view.pending}
      <p role="status" class="text-sm">
        {view.phase === "applying"
          ? "Applying this change…"
          : view.phase === "recovering"
            ? "Checking whether this change was saved…"
            : "The save outcome is unknown."}
      </p>
      <p class="text-xs text-muted-foreground break-all">
        Recovery key: {view.pending.idempotencyKey}. {view.pendingPersisted
          ? "This key is kept for this tab and survives a reload; copy it if you need to resolve the change elsewhere."
          : "This browser is not storing the key, so it is lost if you reload — copy it now."}
        <Button
          variant="ghost"
          onclick={() => {
            navigator.clipboard?.writeText(view.pending?.idempotencyKey ?? "");
            recoveryKeyCopied = true;
          }}>{recoveryKeyCopied ? "Copied" : "Copy key"}</Button
        >
      </p>
      {#if view.phase === "uncertain"}
        <div class="flex gap-2 flex-wrap">
          <Button variant="outline" onclick={() => session.recover()}>Check saved change</Button>
          <Button variant="outline" onclick={() => session.recover(true)}
            >Check and retry same change</Button
          >
          <Button variant="ghost" onclick={() => session.abandon()}>Abandon recovery key</Button>
        </div>
      {/if}
    {/if}
  </div>

  {#if view.policy}
    <div class="flex border-y border-border" aria-label="Work type">
      <Button
        class="flex-1 rounded-none"
        variant={verb === "SERVE" ? "secondary" : "ghost"}
        aria-pressed={verb === "SERVE"}
        onclick={() => switchVerb("SERVE")}>Viewer delivery</Button
      >
      <Button
        class="flex-1 rounded-none"
        variant={verb === "INGEST" ? "secondary" : "ghost"}
        aria-pressed={verb === "INGEST"}
        onclick={() => switchVerb("INGEST")}>Ingest</Button
      >
    </div>
    <div class="grid grid-cols-1 lg:grid-cols-2">
      <div class="p-4 md:p-6 min-w-0 space-y-4 lg:border-r border-border">
        {#if scope.kind === "STREAM" && verbPolicy?.inheritedRules}
          <details class="text-sm border border-border p-3">
            <summary class="cursor-pointer"
              >Locked account restrictions · revision {view.policy.parentRevision}</summary
            >
            <p class="mt-2 text-muted-foreground">
              Stream overrides may narrow these restrictions, never remove them. Local preference
              groups replace account preference order.
            </p>
            <ul class="mt-2 space-y-1">
              {#if verbPolicy.inheritedRules.constraints.allow}
                <li>
                  Allowed alternatives: {verbPolicy.inheritedRules.constraints.allow.any.length ===
                  0
                    ? "none (deny all)"
                    : verbPolicy.inheritedRules.constraints.allow.any
                        .map(selectorLabel)
                        .join("; OR ")}
                </li>
              {/if}
              {#each verbPolicy.inheritedRules.constraints.deny as selector, index (index)}<li>
                  Never use: {selectorLabel(selector)}
                </li>{/each}
            </ul>
          </details>
        {/if}
        {#key `${scopeKey}:${verb}`}
          <PlacementRulesEditor
            rules={view.drafts[verb]}
            {scope}
            features={view.policy.features}
            disabled={locked}
            onchange={(rules) => session.edit(verb, rules)}
          />
        {/key}
        {#if !view.drafts[verb]}
          <details class="text-sm">
            <summary class="cursor-pointer">Requested effective preference order</summary>
            <ol class="list-decimal pl-5 mt-2 space-y-1">
              {#each verbPolicy?.requestedEffective.groups ?? [] as group (group.id)}<li>
                  {selectorLabel(group.match)}
                </li>{/each}
            </ol>
            <p class="mt-2 text-xs text-muted-foreground">
              Requested intent, not proof of current routing or enforcement.
            </p>
          </details>
        {/if}
      </div>

      <div class="p-4 md:p-6 space-y-5 border-t lg:border-t-0 border-border min-w-0">
        {#if view.review}
          <section class="space-y-4" aria-label="Review placement changes">
            <h3 class="font-semibold">Review changes · both work tabs</h3>
            <div class="space-y-3">
              {#each view.review.differences as difference, index (index)}
                <div class="border-t border-border pt-3 text-sm">
                  <h4 class="font-medium">{difference.label}</h4>
                  <p class="text-muted-foreground break-words">Before: {difference.before}</p>
                  <p class="break-words">After: {difference.after}</p>
                </div>
              {/each}
            </div>
            <p class="text-sm">
              {view.review.impact.complete
                ? `${view.review.impact.affectedStreams} affected stream(s), ${view.review.impact.activePublishers} active publisher(s).`
                : "Impact assessment is partial; these counts do not certify all affected streams or cells."}
            </p>
            {#if view.review.impact.existingSessionsRetained}<p
                class="text-sm text-muted-foreground"
              >
                Existing admitted sessions are retained.
              </p>{/if}
            {#each view.review.warnings as warning (warning.id)}
              {#if warning.acknowledgementRequired}
                <label class="flex items-start gap-2 text-sm"
                  ><input
                    class="mt-1"
                    type="checkbox"
                    disabled={!!view.pending}
                    checked={view.acknowledgements.includes(warning.id)}
                    onchange={(event) =>
                      session.acknowledge(warning.id, event.currentTarget.checked)}
                  />{warning.message}</label
                >
              {:else}<p class="text-sm">{warning.message}</p>{/if}
            {/each}
            <p class="text-xs text-muted-foreground">
              {reviewExpired
                ? "Review expired. Review again before applying."
                : `Review expires ${view.review.expiresAt}. Access and revisions are checked again when applying.`}
            </p>
            <Button disabled={!canApply} onclick={() => session.apply()}>Apply rules</Button>
          </section>
        {/if}
        <section class="space-y-3" aria-label="Placement preview">
          <h3 class="font-semibold">Preview {verb === "SERVE" ? "viewer delivery" : "ingest"}</h3>
          <p class="text-sm text-muted-foreground">
            A read-only, hypothetical decision. It never reserves capacity, creates a source pull or
            moves an active publisher.
          </p>
          {#if scope.kind === "TENANT"}<label class="block text-sm"
              >Owned stream ID (optional)<Input
                bind:value={previewStreamId}
                oninput={changePreviewInput}
                placeholder="Omit for capacity-only preview"
              /></label
            >{/if}
          <label class="block text-sm"
            >Protocol
            <select
              class="block w-full mt-1 p-2 bg-background border border-border"
              bind:value={protocol}
              onchange={changePreviewInput}
            >
              <option value="">Default</option>
              {#each verb === "SERVE" ? ["hls", "dash", "webrtc"] : ["rtmp", "srt", "whip"] as item (item)}<option
                  value={item}>{item.toUpperCase()}</option
                >{/each}
            </select>
          </label>
          <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
            <label class="text-sm"
              >Latitude<Input
                inputmode="decimal"
                bind:value={latitude}
                oninput={changePreviewInput}
                placeholder="Unknown"
              /></label
            >
            <label class="text-sm"
              >Longitude<Input
                inputmode="decimal"
                bind:value={longitude}
                oninput={changePreviewInput}
                placeholder="Unknown"
              /></label
            >
          </div>
          {#if previewError}<p role="alert" class="text-sm text-destructive">{previewError}</p>{/if}
          <Button
            variant="outline"
            disabled={!view.policy.actions.canPreview || view.previewing || !!view.pending}
            onclick={preview}>{view.previewing ? "Previewing…" : "Preview this draft"}</Button
          >
          {#if view.preview}
            <div class="space-y-3 text-sm border-t border-border pt-3" aria-live="polite">
              {#if view.previewStale || previewExpired}<p class="text-warning">
                  {view.previewStale
                    ? "Inputs changed. This preview is stale."
                    : "This observation has expired. Preview again."}
                </p>{/if}
              <p class="font-medium">
                {view.preview.selected
                  ? `Selected: ${view.preview.selected.clusterName}`
                  : view.preview.complete
                    ? "No eligible destination"
                    : "Cannot determine a destination"}
              </p>
              <p>{view.preview.reason}</p>
              {#if !view.preview.complete}<p class="text-warning">
                  Partial observations. Missing cells are not proven empty or full.
                </p>{/if}
              {#if !view.preview.sourceEvaluated}<p>
                  Source path was not evaluated. This is not a playable-route prediction.
                </p>{:else}<p>
                  Source path observed. This preview did not reserve capacity or start media.
                </p>{/if}
              {#if view.preview.activeIngestClusterId}<p>
                  Active publisher is pinned to cluster {view.preview.activeIngestClusterId}.
                </p>{/if}
              {#if view.preview.selected?.requiresSourcePull}<p>
                  The selected destination needs a source pull. Preview did not start one.
                </p>{/if}
              {#each view.preview.transitions as transition, index (index)}<p>
                  From {transition.fromGroup}: {transition.reason}
                </p>{/each}
              <details>
                <summary class="cursor-pointer"
                  >Candidate reasons ({view.preview.candidates.length})</summary
                >
                <ul class="space-y-3 mt-3">
                  {#each view.preview.candidates as candidate, index (index)}
                    <li class="border-t border-border pt-2">
                      <p>
                        {candidate.clusterName}
                        {candidate.region ? `· ${candidate.region}` : ""}{candidate.distanceKm !==
                        null
                          ? ` · ${Math.round(candidate.distanceKm)} km`
                          : " · distance unknown"}
                      </p>
                      <p class="text-muted-foreground">{candidate.reason}</p>
                      {#if candidate.nodeId}<p class="text-xs">Node: {candidate.nodeId}</p>{/if}
                      {#if candidate.price}<p class="text-xs">
                          {candidate.price.amountMicros} micro-{candidate.price.currency} / {candidate
                            .price.unit} · pricing revision {candidate.price.revision} · expires {candidate
                            .price.expiresAt}
                        </p>{/if}
                    </li>
                  {/each}
                </ul>
              </details>
              <p class="text-xs text-muted-foreground">
                Observed {view.preview.observedAt}; expires {view.preview.expiresAt}.
              </p>
              <p class="text-xs text-muted-foreground break-all">
                Draft digest: {view.preview.digest}
              </p>
            </div>
          {/if}
        </section>
      </div>
    </div>
    <p role="status" class="slab-body--padded text-sm">
      {dirty ? "Unsaved changes" : "No unsaved changes"}
    </p>
    <div class="slab-actions slab-actions--row flex-wrap">
      {#if discardPending}
        <Button
          variant="ghost"
          onclick={() => {
            session.discard();
            discardPending = false;
          }}>Confirm discard both tabs</Button
        >
        <Button variant="ghost" onclick={() => (discardPending = false)}>Cancel</Button>
      {:else}<Button
          variant="ghost"
          disabled={!dirty || locked || view.conflict}
          onclick={() => (discardPending = true)}>Discard</Button
        >{/if}
      <Button
        variant="ghost"
        disabled={!dirty || locked || view.phase === "reviewing" || view.conflict}
        onclick={() => session.review()}
        >{view.phase === "reviewing" ? "Checking changes…" : "Review changes"}</Button
      >
    </div>
  {/if}
</div>
