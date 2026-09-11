<script lang="ts">
  import { onDestroy, untrack } from "svelte";
  import { auth } from "$lib/stores/auth";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import {
    destinationHost,
    ingestProtocolUrl,
    splitRtmpDestination,
    type ResolvedIngest,
  } from "$lib/placement/ingest";
  import { resolveIngestDestination } from "$lib/placement/ingest-api";

  let { streamId, streamKey }: { streamId: string; streamKey: string } = $props();
  let result = $state<ResolvedIngest | null>(null);
  let resolving = $state(false);
  let error = $state("");
  let copied = $state("");
  let observedAt = $state("");
  let reveal = $state(false);
  let requestedProtocol = $state<"" | "WHIP" | "RTMP" | "SRT">("");
  let sequence = 0;
  let requestAbort: AbortController | null = null;
  const identity = $derived(`${$auth.user?.tenant_id ?? ""}:${$auth.user?.id ?? ""}`);
  const canResolve = $derived($auth.isAuthenticated && !!$auth.user?.tenant_id && !!$auth.user?.id);
  const rtmp = $derived(result ? splitRtmpDestination(result.primary, streamKey) : null);
  $effect(() => {
    const binding = `${identity}:${canResolve}:${streamId}:${streamKey}:${requestedProtocol}`;
    untrack(() => {
      if (binding) sequence++;
      requestAbort?.abort();
      result = null;
      resolving = false;
      error = "";
      observedAt = "";
      reveal = false;
      copied = "";
    });
  });
  onDestroy(() => {
    sequence++;
    requestAbort?.abort();
  });

  async function resolveDestination() {
    if (!canResolve || !streamKey || !streamId || resolving) return;
    const request = ++sequence;
    requestAbort?.abort();
    requestAbort = new AbortController();
    resolving = true;
    result = null;
    error = "";
    copied = "";
    reveal = false;
    try {
      const resolved = await resolveIngestDestination(
        streamId,
        streamKey,
        requestAbort.signal,
        requestedProtocol || undefined
      );
      if (request !== sequence) return;
      result = resolved;
      observedAt = new Date().toLocaleTimeString();
    } catch {
      if (request === sequence)
        error =
          "Ingest resolution failed. No generic address has been substituted. Check access and capacity, then retry.";
    } finally {
      if (request === sequence) resolving = false;
    }
  }

  async function copy(value: string, label: string) {
    try {
      await navigator.clipboard.writeText(value);
      copied = label;
    } catch {
      error = "Could not copy to the clipboard.";
    }
  }
</script>

<section class="slab col-span-full [&_button]:min-h-11" aria-label="Recommended ingest destination">
  <div class="slab-header"><h3>Recommended destination</h3></div>
  <div class="slab-body--padded space-y-3">
    <p class="text-sm text-muted-foreground">
      Resolve for this publisher’s connection. This uses the ingest resolver; it is not a
      hypothetical placement preview.
    </p>
    <label class="block text-sm space-y-1">
      <span>Publisher protocol</span>
      <select
        bind:value={requestedProtocol}
        class="block w-full min-h-11 border border-input bg-background px-3 py-2"
      >
        <option value="">Any advertised protocol</option>
        <option value="WHIP">WHIP — browser or WebRTC encoder</option>
        <option value="RTMP">RTMP / E-RTMP — encoder</option>
        <option value="SRT">SRT — encoder</option>
      </select>
    </label>
    <p class="text-xs text-muted-foreground">
      Choose your encoder’s protocol to exclude nodes that cannot accept it. Changing this choice
      clears the previous recommendation; resolve again before connecting.
    </p>
    <Button
      variant="outline"
      disabled={!canResolve || resolving || !streamId || !streamKey}
      onclick={resolveDestination}
      >{resolving ? "Resolving…" : result ? "Refresh destination" : "Resolve destination"}</Button
    >
    {#if error}<p role="alert" class="text-sm text-destructive">{error}</p>{/if}
    {#if result}
      <p class="font-medium break-all">
        {destinationHost(result.primary)} · {result.primary.region ?? "Region unavailable"}
      </p>
      <p class="text-xs text-muted-foreground">
        Resolved at {observedAt}. Advisory only: refresh before connecting. This is not admission or
        a node reservation. Policy rollout and active ingest claim status are not included in this
        response.
      </p>
      <label class="flex items-center gap-2 text-sm min-h-11"
        ><input type="checkbox" bind:checked={reveal} />Show credential-bearing connection URLs</label
      >
      {#if rtmp}
        <div class="grid grid-cols-1 md:grid-cols-2 gap-3">
          <div class="space-y-2">
            <label class="text-sm block">RTMP server<Input readonly value={rtmp.server} /></label
            ><Button size="sm" variant="outline" onclick={() => copy(rtmp.server, "RTMP server")}
              >Copy RTMP server</Button
            >
          </div>
          <div class="space-y-2">
            <label class="text-sm block"
              >RTMP stream key<Input
                readonly
                type={reveal ? "text" : "password"}
                value={rtmp.key}
              /></label
            ><Button size="sm" variant="outline" onclick={() => copy(rtmp.key, "RTMP key")}
              >Copy stream key</Button
            >
          </div>
        </div>
      {/if}
      {#each ["whip", "srt", ...(!rtmp ? ["rtmp"] : [])] as protocol (protocol)}
        {@const url = ingestProtocolUrl(result.primary, protocol as "rtmp" | "srt" | "whip")}
        {#if url}
          <div class="flex flex-wrap items-end gap-2">
            <label class="text-sm flex-1 min-w-0"
              >{protocol.toUpperCase()} URL<Input
                readonly
                type={reveal ? "text" : "password"}
                value={url}
              /></label
            ><Button size="sm" variant="outline" onclick={() => copy(url, protocol.toUpperCase())}
              >Copy {protocol.toUpperCase()}</Button
            >
          </div>
        {/if}
      {/each}
      {#if copied}<p role="status" class="text-xs">{copied} copied.</p>{/if}
    {/if}
  </div>
</section>
