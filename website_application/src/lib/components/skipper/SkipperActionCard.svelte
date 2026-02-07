<script lang="ts">
  import { getIconComponent } from "$lib/iconUtils";

  interface Props {
    toolName: string;
    payload: Record<string, unknown>;
  }

  let { toolName, payload }: Props = $props();

  const CheckIcon = getIconComponent("Check");
  const XIcon = getIconComponent("X");

  const actionLabels: Record<string, string> = {
    create_clip: "Clip Created",
    delete_clip: "Clip Deleted",
    start_dvr: "DVR Started",
    stop_dvr: "DVR Stopped",
    create_vod_upload: "VOD Upload Created",
    complete_vod_upload: "VOD Upload Completed",
    abort_vod_upload: "VOD Upload Aborted",
    delete_vod_asset: "VOD Asset Deleted",
    topup_balance: "Balance Top-up Initiated",
    check_topup: "Top-up Status",
    resolve_playback_endpoint: "Playback Resolved",
    update_billing_details: "Billing Updated",
    get_payment_options: "Payment Options",
    submit_payment: "Payment Submitted",
  };

  const sensitivePatterns = /key|secret|token|password/i;

  const isError = !!(payload.error || payload.Error);
  const errorMessage = (payload.error || payload.Error || "") as string;

  function formatKey(key: string): string {
    return key
      .replace(/_/g, " ")
      .replace(/([a-z])([A-Z])/g, "$1 $2")
      .replace(/^./, (c) => c.toUpperCase());
  }

  function formatValue(value: unknown): string {
    if (value === null || value === undefined) return "N/A";
    if (typeof value === "boolean") return value ? "Yes" : "No";
    if (typeof value === "number") {
      if (Number.isInteger(value)) return value.toLocaleString();
      return value.toFixed(2);
    }
    return String(value);
  }

  function isSimpleValue(value: unknown): boolean {
    return (
      value === null ||
      value === undefined ||
      typeof value === "string" ||
      typeof value === "number" ||
      typeof value === "boolean"
    );
  }

  let showSensitive = $state(false);

  const entries = Object.entries(payload).filter(
    ([key, value]) =>
      value !== null && value !== undefined && value !== "" && key !== "error" && key !== "Error"
  );
</script>

<div class="rounded-lg border border-border bg-card text-sm">
  <div class="flex items-center gap-2 border-b border-border px-4 py-2.5">
    {#if isError}
      <div class="flex h-5 w-5 items-center justify-center rounded-full bg-red-500/10 text-red-500">
        <XIcon class="h-3 w-3" />
      </div>
      <span class="font-semibold text-foreground">
        {actionLabels[toolName] ?? toolName} — Failed
      </span>
    {:else}
      <div
        class="flex h-5 w-5 items-center justify-center rounded-full bg-emerald-500/10 text-emerald-500"
      >
        <CheckIcon class="h-3 w-3" />
      </div>
      <span class="font-semibold text-foreground">
        {actionLabels[toolName] ?? toolName}
      </span>
    {/if}
  </div>

  {#if isError}
    <div class="px-4 py-2.5">
      <p class="text-xs text-red-500">{errorMessage}</p>
    </div>
  {:else if entries.length > 0}
    <div class="divide-y divide-border">
      {#each entries as [key, value] (key)}
        <div class="flex items-start justify-between gap-4 px-4 py-2">
          <span class="shrink-0 text-xs text-muted-foreground">{formatKey(key)}</span>
          {#if isSimpleValue(value)}
            {#if sensitivePatterns.test(key)}
              <div class="flex items-center gap-2">
                <span class="truncate font-mono text-xs text-foreground">
                  {showSensitive ? formatValue(value) : "\u25CF".repeat(8)}
                </span>
                <button
                  type="button"
                  class="shrink-0 text-[10px] text-primary hover:text-primary/80"
                  onclick={() => (showSensitive = !showSensitive)}
                >
                  {showSensitive ? "Hide" : "Show"}
                </button>
              </div>
            {:else}
              <span class="truncate text-right font-mono text-xs text-foreground">
                {formatValue(value)}
              </span>
            {/if}
          {:else}
            <pre
              class="max-h-24 overflow-auto rounded bg-muted/40 px-2 py-1 text-[10px] text-foreground">{JSON.stringify(
                value,
                null,
                2
              )}</pre>
          {/if}
        </div>
      {/each}
    </div>
  {/if}
</div>
