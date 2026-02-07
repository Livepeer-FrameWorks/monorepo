<script lang="ts">
  import type { SkipperDetail } from "./SkipperMessage.svelte";
  import SkipperDiagnosticCard from "./SkipperDiagnosticCard.svelte";
  import SkipperStreamCard from "./SkipperStreamCard.svelte";
  import SkipperSearchCard from "./SkipperSearchCard.svelte";
  import SkipperCodeCard from "./SkipperCodeCard.svelte";
  import SkipperActionCard from "./SkipperActionCard.svelte";

  interface Props {
    detail: SkipperDetail;
  }

  let { detail }: Props = $props();

  const diagnosticTools = new Set([
    "diagnose_rebuffering",
    "diagnose_buffer_health",
    "diagnose_packet_loss",
    "diagnose_routing",
    "get_stream_health_summary",
    "get_anomaly_report",
  ]);

  const streamTools = new Set([
    "create_stream",
    "update_stream",
    "delete_stream",
    "refresh_stream_key",
  ]);

  const searchTools = new Set(["search_knowledge", "search_web", "search_support_history"]);

  const codeTools = new Set(["introspect_schema", "generate_query", "execute_query"]);

  const actionTools = new Set([
    "create_clip",
    "delete_clip",
    "start_dvr",
    "stop_dvr",
    "create_vod_upload",
    "complete_vod_upload",
    "abort_vod_upload",
    "delete_vod_asset",
    "topup_balance",
    "check_topup",
    "resolve_playback_endpoint",
    "update_billing_details",
    "get_payment_options",
    "submit_payment",
  ]);

  function extractToolName(title?: string): string {
    if (!title) return "";
    const match = title.match(/^Tool call:\s*(.+)$/i);
    return match ? match[1].trim() : "";
  }

  function getPayload(): Record<string, unknown> {
    if (typeof detail.payload === "string") {
      try {
        return JSON.parse(detail.payload) as Record<string, unknown>;
      } catch {
        return {};
      }
    }
    return detail.payload;
  }

  const toolName = extractToolName(detail.title);
  const payload = getPayload();

  function formatFallback(): string {
    if (typeof detail.payload === "string") return detail.payload;
    return JSON.stringify(detail.payload, null, 2);
  }
</script>

{#if toolName && diagnosticTools.has(toolName)}
  <SkipperDiagnosticCard {toolName} {payload} />
{:else if toolName && streamTools.has(toolName)}
  <SkipperStreamCard {toolName} {payload} />
{:else if toolName && searchTools.has(toolName)}
  <SkipperSearchCard {toolName} {payload} />
{:else if toolName && codeTools.has(toolName)}
  <SkipperCodeCard {toolName} {payload} />
{:else if toolName && actionTools.has(toolName)}
  <SkipperActionCard {toolName} {payload} />
{:else}
  <details
    class="rounded-md border border-border bg-muted/40 px-3 py-2 text-xs text-muted-foreground"
  >
    <summary class="cursor-pointer font-semibold uppercase tracking-[0.16em] text-[10px]">
      {detail.title || "Details"}
    </summary>
    <pre
      class="mt-2 whitespace-pre-wrap rounded-md border border-border bg-background/80 p-2 text-[11px] text-foreground">{formatFallback()}</pre>
  </details>
{/if}
