<script lang="ts">
  import type { SkipperDetail } from "./SkipperMessage.svelte";

  interface Props {
    detail: SkipperDetail;
  }

  let { detail }: Props = $props();

  interface SearchResult {
    title?: string;
    Title?: string;
    url?: string;
    URL?: string;
    snippet?: string;
    Snippet?: string;
    content?: string;
    Content?: string;
    score?: number;
    Score?: number;
    similarity?: number;
    Similarity?: number;
  }

  const diagnosticTools = new Set([
    "diagnose_rebuffering",
    "diagnose_buffer_health",
    "diagnose_packet_loss",
    "diagnose_routing",
    "get_stream_health_summary",
    "get_anomaly_report",
  ]);

  const searchTools = new Set(["search_knowledge", "search_web"]);

  const diagnosticLabels: Record<string, string> = {
    diagnose_rebuffering: "Rebuffering Diagnosis",
    diagnose_buffer_health: "Buffer Health",
    diagnose_packet_loss: "Packet Loss Analysis",
    diagnose_routing: "Routing Diagnostics",
    get_stream_health_summary: "Stream Health Summary",
    get_anomaly_report: "Anomaly Detection",
  };

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
  const isSearch = searchTools.has(toolName);
  const isDiagnostic = diagnosticTools.has(toolName);

  // Search helpers
  function getResults(): SearchResult[] {
    if (Array.isArray(payload.results)) return payload.results as SearchResult[];
    if (Array.isArray(payload.Results)) return payload.Results as SearchResult[];
    return [];
  }

  function getTitle(r: SearchResult): string {
    return r.title || r.Title || "Untitled";
  }

  function getUrl(r: SearchResult): string {
    return r.url || r.URL || "";
  }

  function getSnippet(r: SearchResult): string {
    const raw = r.snippet || r.Snippet || r.content || r.Content || "";
    if (raw.length > 200) return raw.slice(0, 200) + "\u2026";
    return raw;
  }

  function getScore(r: SearchResult): number | null {
    const val = r.score ?? r.Score ?? r.similarity ?? r.Similarity ?? null;
    return typeof val === "number" ? val : null;
  }

  // Diagnostic helpers
  const diagStatus = (payload.status as string) ?? "unknown";
  const diagMetrics = (payload.metrics as Record<string, unknown>) ?? {};
  const diagAnalysis = (payload.analysis as string) ?? "";
  const diagRecommendations = (payload.recommendations as string[]) ?? [];

  function formatMetricKey(key: string): string {
    return key
      .replace(/_/g, " ")
      .replace(/([a-z])([A-Z])/g, "$1 $2")
      .replace(/^./, (c) => c.toUpperCase());
  }

  function formatMetricValue(value: unknown): string {
    if (typeof value === "number") {
      if (Number.isInteger(value)) return value.toLocaleString();
      return value.toFixed(2);
    }
    if (typeof value === "boolean") return value ? "Yes" : "No";
    if (value === null || value === undefined) return "N/A";
    return String(value);
  }

  // Fallback
  function formatFallback(): string {
    if (typeof detail.payload === "string") return detail.payload;
    return JSON.stringify(detail.payload, null, 2);
  }
</script>

{#if isSearch}
  {@const results = getResults()}
  <div class="docs-skipper-tool-card">
    <div class="docs-skipper-tool-card__header">
      <span class="docs-skipper-tool-card__title">
        {toolName === "search_web" ? "Web Search" : "Knowledge Base"}
      </span>
      {#if results.length > 0}
        <span class="docs-skipper-tool-card__count">
          {results.length} result{results.length === 1 ? "" : "s"}
        </span>
      {/if}
    </div>
    {#if results.length === 0}
      <div class="docs-skipper-tool-card__empty">No results found.</div>
    {:else}
      <div class="docs-skipper-tool-card__results">
        {#each results as result, i (i)}
          <div class="docs-skipper-tool-card__result">
            <div class="docs-skipper-tool-card__result-header">
              <span class="docs-skipper-tool-card__result-num">{i + 1}.</span>
              {#if getUrl(result)}
                <a
                  class="docs-skipper-tool-card__result-link"
                  href={getUrl(result)}
                  target="_blank"
                  rel="noreferrer"
                >
                  {getTitle(result)}
                </a>
              {:else}
                <span class="docs-skipper-tool-card__result-title">{getTitle(result)}</span>
              {/if}
              {#if getScore(result) !== null}
                <span class="docs-skipper-tool-card__score">{getScore(result)?.toFixed(2)}</span>
              {/if}
            </div>
            {#if getUrl(result)}
              <div class="docs-skipper-tool-card__result-url">{getUrl(result)}</div>
            {/if}
            {#if getSnippet(result)}
              <div class="docs-skipper-tool-card__result-snippet">{getSnippet(result)}</div>
            {/if}
          </div>
        {/each}
      </div>
    {/if}
  </div>
{:else if isDiagnostic}
  <div class="docs-skipper-tool-card">
    <div class="docs-skipper-tool-card__header">
      <span class="docs-skipper-tool-card__title">
        {diagnosticLabels[toolName] ?? toolName}
      </span>
      <span class="docs-skipper-tool-card__status docs-skipper-tool-card__status--{diagStatus}">
        {diagStatus.charAt(0).toUpperCase() + diagStatus.slice(1)}
      </span>
    </div>

    {#if Object.keys(diagMetrics).length > 0}
      <div class="docs-skipper-tool-card__section">
        <div class="docs-skipper-tool-card__label">Metrics</div>
        {#each Object.entries(diagMetrics) as [key, value] (key)}
          <div class="docs-skipper-tool-card__metric">
            <span class="docs-skipper-tool-card__metric-key">{formatMetricKey(key)}</span>
            <span class="docs-skipper-tool-card__metric-value">{formatMetricValue(value)}</span>
          </div>
        {/each}
      </div>
    {/if}

    {#if diagAnalysis}
      <div class="docs-skipper-tool-card__section">
        <div class="docs-skipper-tool-card__label">Analysis</div>
        <p class="docs-skipper-tool-card__text">{diagAnalysis}</p>
      </div>
    {/if}

    {#if diagRecommendations.length > 0}
      <div class="docs-skipper-tool-card__section">
        <div class="docs-skipper-tool-card__label">Recommendations</div>
        <ul class="docs-skipper-tool-card__recs">
          {#each diagRecommendations as rec (rec)}
            <li>{rec}</li>
          {/each}
        </ul>
      </div>
    {/if}
  </div>
{:else}
  <details class="docs-skipper-message__details">
    <summary class="docs-skipper-message__details-summary">
      {detail.title || "Details"}
    </summary>
    <div class="docs-skipper-message__details-body">
      <pre class="docs-skipper-message__details-code">{formatFallback()}</pre>
    </div>
  </details>
{/if}
