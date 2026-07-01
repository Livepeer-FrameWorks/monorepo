<script lang="ts">
  import { SvelteMap } from "svelte/reactivity";

  interface CapabilityPrice {
    capability: string;
    pricePerUnitEth: string;
    pixelsPerUnit: string;
  }

  interface OrchestratorIdentity {
    orchAddr: string;
    lastSeen: string;
    updatedAt: string;
  }

  interface OrchestratorInstance {
    orchAddr: string;
    resolvedIp: string;
    canonicalUrl: string;
    advertisedNodeUrls: string[];
    capabilities: string[];
    pricePerUnitEth: string;
    pixelsPerUnit: string;
    capabilityPrices: CapabilityPrice[];
    hardware: string;
    source: string;
    lastSeen: string;
    updatedAt: string;
  }

  interface OrchestratorVantage {
    gatewayId: string;
    gatewayRegion: string;
    orchAddr: string;
    resolvedIp: string;
    latitude: number;
    longitude: number;
    city: string;
    countryCode: string;
    geoSource: string;
    latestLatencyMs: number;
    score: number;
    dialedRecently: boolean;
    lastSeen: string;
  }

  interface OrchestratorPerformancePoint {
    attempts: number;
    successes: number;
    failures: number;
    meanLatencyMs: number;
    transcodeAttempts: number;
    transcodeSuccesses: number;
    transcodeFailures: number;
    transcodeMeanOverallMs: number;
    transcodePixels: number;
    aiAttempts: number;
    aiSuccesses: number;
    aiFailures: number;
    aiMeanLatencyMs: number;
  }

  interface Props {
    orchestrator: OrchestratorIdentity | null | undefined;
    instances: OrchestratorInstance[];
    vantages: OrchestratorVantage[];
    performancePoints: OrchestratorPerformancePoint[];
    onClose?: () => void;
  }

  let {
    orchestrator,
    instances = [],
    vantages = [],
    performancePoints = [],
    onClose,
  }: Props = $props();

  // Group vantages by resolved_ip so the per-region table folds matching
  // gateway observations together. Same resolved IP across N gateways =
  // genuinely the same instance, but the latency varies per gateway —
  // surface both.
  let vantagesByIp = $derived.by(() => {
    const map = new SvelteMap<string, OrchestratorVantage[]>();
    for (const v of vantages) {
      const list = map.get(v.resolvedIp) ?? [];
      list.push(v);
      map.set(v.resolvedIp, list);
    }
    return map;
  });

  function instanceForIp(ip: string): OrchestratorInstance | undefined {
    return instances.find((i) => i.resolvedIp === ip);
  }

  function formatLatency(ms: number, dialedRecently: boolean): string {
    if (!dialedRecently) return "stale";
    return `${ms} ms`;
  }

  function priceLabel(inst: OrchestratorInstance): string {
    const ppu = String(inst.pricePerUnitEth);
    const pixels = String(inst.pixelsPerUnit);
    if (ppu === "0" || pixels === "0") return "—";
    return `${ppu} ETH / ${pixels} pixels`;
  }

  function capabilityPriceLabel(price: CapabilityPrice): string {
    if (price.pricePerUnitEth === "0" || price.pixelsPerUnit === "0") return "—";
    return `${price.pricePerUnitEth} ETH / ${price.pixelsPerUnit} px`;
  }

  let performanceSummary = $derived.by(() => {
    const summary = {
      discoveryAttempts: 0,
      discoverySuccesses: 0,
      transcodeAttempts: 0,
      transcodeSuccesses: 0,
      transcodeFailures: 0,
      transcodePixels: 0,
      transcodeOverallWeightedMs: 0,
      aiAttempts: 0,
      aiSuccesses: 0,
      aiFailures: 0,
      aiLatencyWeightedMs: 0,
    };
    for (const point of performancePoints) {
      summary.discoveryAttempts += point.attempts;
      summary.discoverySuccesses += point.successes;
      summary.transcodeAttempts += point.transcodeAttempts;
      summary.transcodeSuccesses += point.transcodeSuccesses;
      summary.transcodeFailures += point.transcodeFailures;
      summary.transcodePixels += point.transcodePixels;
      summary.transcodeOverallWeightedMs += point.transcodeMeanOverallMs * point.transcodeSuccesses;
      summary.aiAttempts += point.aiAttempts;
      summary.aiSuccesses += point.aiSuccesses;
      summary.aiFailures += point.aiFailures;
      summary.aiLatencyWeightedMs += point.aiMeanLatencyMs * point.aiSuccesses;
    }
    return summary;
  });

  function percent(successes: number, attempts: number): string {
    if (attempts <= 0) return "—";
    return `${((successes / attempts) * 100).toFixed(1)}%`;
  }

  function weightedMs(total: number, count: number): string {
    if (count <= 0) return "—";
    return `${(total / count).toFixed(0)} ms`;
  }
</script>

<aside class="orch-panel" aria-label="Orchestrator detail">
  <header class="orch-panel__header">
    <div>
      <h3 class="orch-panel__title">Orchestrator</h3>
      {#if orchestrator}
        <code class="orch-panel__addr">{orchestrator.orchAddr}</code>
      {/if}
    </div>
    {#if onClose}
      <button type="button" class="orch-panel__close" onclick={onClose} aria-label="Close">
        ×
      </button>
    {/if}
  </header>

  {#if !orchestrator}
    <p class="orch-panel__empty">No orchestrator selected.</p>
  {:else}
    <section class="orch-panel__section">
      <h4>Identity</h4>
      <dl class="orch-panel__dl">
        <dt>Last seen</dt>
        <dd>{orchestrator.lastSeen}</dd>
        <dt>Instances</dt>
        <dd>{instances.length}</dd>
        <dt>Vantages</dt>
        <dd>{vantages.length}</dd>
      </dl>
    </section>

    <section class="orch-panel__section">
      <h4>Instances ({instances.length})</h4>
      {#if instances.length === 0}
        <p class="orch-panel__empty">No instances observed yet.</p>
      {:else}
        <table class="orch-panel__table">
          <thead>
            <tr>
              <th>IP</th>
              <th>Hardware</th>
              <th>Price</th>
              <th>Capabilities</th>
              <th>Last seen</th>
            </tr>
          </thead>
          <tbody>
            {#each instances as inst (inst.resolvedIp)}
              <tr>
                <td><code>{inst.resolvedIp}</code></td>
                <td>{inst.hardware || "—"}</td>
                <td>{priceLabel(inst)}</td>
                <td>
                  {#if inst.capabilities.length === 0}
                    —
                  {:else}
                    <div class="orch-panel__chips">
                      {#each inst.capabilities as cap (cap)}
                        <span class="orch-panel__chip">{cap}</span>
                      {/each}
                    </div>
                  {/if}
                </td>
                <td>{inst.lastSeen}</td>
              </tr>
              {#if inst.capabilityPrices.length > 0 || inst.advertisedNodeUrls.length > 0}
                <tr class="orch-panel__detail-row">
                  <td colspan="5">
                    {#if inst.capabilityPrices.length > 0}
                      <div class="orch-panel__detail-block">
                        <span class="orch-panel__detail-label">Capability prices</span>
                        <div class="orch-panel__chips">
                          {#each inst.capabilityPrices as price (price.capability)}
                            <span class="orch-panel__chip"
                              >{price.capability}: {capabilityPriceLabel(price)}</span
                            >
                          {/each}
                        </div>
                      </div>
                    {/if}
                    {#if inst.advertisedNodeUrls.length > 0}
                      <div class="orch-panel__detail-block">
                        <span class="orch-panel__detail-label">Advertised nodes</span>
                        <div class="orch-panel__list">
                          {#each inst.advertisedNodeUrls as url (url)}
                            <code>{url}</code>
                          {/each}
                        </div>
                      </div>
                    {/if}
                  </td>
                </tr>
              {/if}
            {/each}
          </tbody>
        </table>
      {/if}
    </section>

    <section class="orch-panel__section">
      <h4>Outcome stats</h4>
      <dl class="orch-panel__dl">
        <dt>Discovery uptime</dt>
        <dd>
          {percent(performanceSummary.discoverySuccesses, performanceSummary.discoveryAttempts)}
          <small class="orch-panel__muted">({performanceSummary.discoveryAttempts} attempts)</small>
        </dd>
        <dt>Transcode success</dt>
        <dd>
          {percent(performanceSummary.transcodeSuccesses, performanceSummary.transcodeAttempts)}
          <small class="orch-panel__muted">({performanceSummary.transcodeAttempts} outcomes)</small>
        </dd>
        <dt>Avg transcode time</dt>
        <dd>
          {weightedMs(
            performanceSummary.transcodeOverallWeightedMs,
            performanceSummary.transcodeSuccesses
          )}
        </dd>
        <dt>Pixels</dt>
        <dd>{performanceSummary.transcodePixels.toLocaleString()}</dd>
        <dt>AI success</dt>
        <dd>
          {percent(performanceSummary.aiSuccesses, performanceSummary.aiAttempts)}
          <small class="orch-panel__muted">({performanceSummary.aiAttempts} outcomes)</small>
        </dd>
        <dt>AI latency</dt>
        <dd>
          {weightedMs(performanceSummary.aiLatencyWeightedMs, performanceSummary.aiSuccesses)}
        </dd>
      </dl>
    </section>

    <section class="orch-panel__section">
      <h4>Per-region observation ({vantages.length})</h4>
      {#if vantages.length === 0}
        <p class="orch-panel__empty">No vantage data yet.</p>
      {:else}
        <table class="orch-panel__table">
          <thead>
            <tr>
              <th>IP</th>
              <th>Gateway</th>
              <th>Region</th>
              <th>Geo</th>
              <th>Latency</th>
              <th>Score</th>
            </tr>
          </thead>
          <tbody>
            {#each Array.from(vantagesByIp.entries()) as [ip, group] (ip)}
              {#each group as v (v.gatewayId + ":" + v.resolvedIp)}
                <tr class:orch-panel__row--stale={!v.dialedRecently}>
                  <td>
                    <code>{ip}</code>
                    {#if instanceForIp(ip) === undefined}
                      <span class="orch-panel__pill" title="No instance state for this IP yet"
                        >no state</span
                      >
                    {/if}
                  </td>
                  <td>{v.gatewayId}</td>
                  <td>{v.gatewayRegion || "—"}</td>
                  <td>
                    {v.city || v.countryCode || "?"}
                    <small class="orch-panel__muted">({v.geoSource})</small>
                  </td>
                  <td>{formatLatency(v.latestLatencyMs, v.dialedRecently)}</td>
                  <td>{v.score.toFixed(2)}</td>
                </tr>
              {/each}
            {/each}
          </tbody>
        </table>
      {/if}
    </section>
  {/if}
</aside>

<style>
  .orch-panel {
    display: flex;
    flex-direction: column;
    gap: 1rem;
    padding: 1rem;
    background: var(--card, #24283b);
    border: 1px solid var(--border, rgba(169, 177, 214, 0.2));
    border-radius: 0.5rem;
    color: var(--foreground, #c0caf5);
  }

  .orch-panel__header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 1rem;
  }

  .orch-panel__title {
    margin: 0;
    font-size: 0.875rem;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--muted-foreground, #a9b1d6);
  }

  .orch-panel__addr {
    display: inline-block;
    margin-top: 0.25rem;
    font-size: 0.875rem;
    word-break: break-all;
  }

  .orch-panel__close {
    background: transparent;
    border: 0;
    color: inherit;
    font-size: 1.5rem;
    line-height: 1;
    cursor: pointer;
  }

  .orch-panel__section {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }

  .orch-panel__section h4 {
    margin: 0;
    font-size: 0.75rem;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--muted-foreground, #a9b1d6);
  }

  .orch-panel__dl {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 0.25rem 1rem;
    margin: 0;
    font-size: 0.875rem;
  }

  .orch-panel__dl dt {
    color: var(--muted-foreground, #a9b1d6);
  }

  .orch-panel__dl dd {
    margin: 0;
  }

  .orch-panel__table {
    width: 100%;
    border-collapse: collapse;
    font-size: 0.8125rem;
  }

  .orch-panel__table th,
  .orch-panel__table td {
    padding: 0.375rem 0.5rem;
    text-align: left;
    border-bottom: 1px solid var(--border, rgba(169, 177, 214, 0.15));
  }

  .orch-panel__table th {
    color: var(--muted-foreground, #a9b1d6);
    font-weight: 500;
  }

  .orch-panel__row--stale td {
    color: var(--muted-foreground, #a9b1d6);
  }

  .orch-panel__detail-row td {
    padding-top: 0;
    background: color-mix(in srgb, var(--card, #24283b) 92%, white);
  }

  .orch-panel__detail-block {
    display: grid;
    grid-template-columns: 8rem 1fr;
    gap: 0.5rem;
    align-items: start;
    margin: 0.25rem 0;
  }

  .orch-panel__detail-label {
    color: var(--muted-foreground, #a9b1d6);
    font-size: 0.75rem;
  }

  .orch-panel__chips {
    display: flex;
    flex-wrap: wrap;
    gap: 0.25rem;
  }

  .orch-panel__chip {
    display: inline-flex;
    align-items: center;
    max-width: 100%;
    padding: 0.0625rem 0.375rem;
    border: 1px solid var(--border, rgba(169, 177, 214, 0.2));
    border-radius: 0.25rem;
    color: var(--foreground, #c0caf5);
    word-break: break-word;
  }

  .orch-panel__list {
    display: grid;
    gap: 0.25rem;
    min-width: 0;
  }

  .orch-panel__list code {
    white-space: normal;
    word-break: break-all;
  }

  .orch-panel__pill {
    display: inline-block;
    margin-left: 0.5rem;
    padding: 0 0.375rem;
    font-size: 0.6875rem;
    line-height: 1.4;
    border: 1px solid currentColor;
    border-radius: 999px;
    color: var(--muted-foreground, #a9b1d6);
  }

  .orch-panel__muted {
    color: var(--muted-foreground, #a9b1d6);
  }

  .orch-panel__empty {
    margin: 0;
    font-size: 0.875rem;
    color: var(--muted-foreground, #a9b1d6);
  }
</style>
