<script lang="ts">
  // Time-to-first-frame waterfall: each boot span laid end-to-end on one timeline,
  // so the total reads as "time to first frame". Replaces the plain span table.
  interface Stage {
    label: string;
    ms: number;
    color: string;
    hint?: string;
  }
  interface Props {
    stages: Stage[];
    cacheHitRatio?: number | null;
  }
  let { stages = [], cacheHitRatio = null }: Props = $props();

  let active = $state<number | null>(null);
  let total = $derived(stages.reduce((sum, s) => sum + (s.ms || 0), 0));
</script>

<div class="boot">
  <div class="boot__head">
    <span class="boot__metric">
      <span class="boot__metric-value">{Math.round(total)}</span>
      <span class="boot__metric-unit">ms to first frame</span>
    </span>
    {#if cacheHitRatio != null}
      <span class="boot__cache">{Math.round(cacheHitRatio * 100)}% cache hit</span>
    {/if}
  </div>

  <div
    class="boot__track"
    role="img"
    aria-label="Boot waterfall, {Math.round(total)} milliseconds total"
  >
    {#each stages as s, i (s.label)}
      <span
        class="boot__seg"
        data-active={active === i ? "" : undefined}
        style="width: {total > 0 ? (s.ms / total) * 100 : 0}%; background: {s.color}"
        onmouseenter={() => (active = i)}
        onmouseleave={() => (active = null)}
        role="presentation"
      ></span>
    {/each}
  </div>

  <ul class="boot__legend">
    {#each stages as s, i (s.label)}
      <li
        class="boot__row"
        data-active={active === i ? "" : undefined}
        onmouseenter={() => (active = i)}
        onmouseleave={() => (active = null)}
      >
        <span class="boot__chip" style="background: {s.color}"></span>
        <span class="boot__row-label">{s.label}</span>
        {#if s.hint}<span class="boot__row-hint">{s.hint}</span>{/if}
        <span class="boot__row-ms">{Math.round(s.ms)} ms</span>
      </li>
    {/each}
  </ul>
</div>

<style>
  .boot {
    display: flex;
    flex-direction: column;
    gap: 0.85rem;
  }
  .boot__head {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 0.75rem;
  }
  .boot__metric {
    display: flex;
    align-items: baseline;
    gap: 0.45rem;
  }
  .boot__metric-value {
    font-size: clamp(1.6rem, 2.6vw, 2.1rem);
    font-weight: 700;
    color: hsl(var(--tn-fg));
    font-variant-numeric: tabular-nums;
  }
  .boot__metric-unit {
    font-size: 0.85rem;
    color: hsl(var(--tn-fg-dark));
  }
  .boot__cache {
    font-size: 0.74rem;
    font-weight: 600;
    color: hsl(var(--tn-green));
  }
  .boot__track {
    display: flex;
    width: 100%;
    height: 0.9rem;
    border-radius: 999px;
    overflow: hidden;
    background: hsl(var(--tn-bg-dark) / 0.6);
  }
  .boot__seg {
    height: 100%;
    transition: filter 0.18s ease;
    box-shadow: inset 0 0 0 1px hsl(var(--tn-bg-dark) / 0.35);
  }
  .boot__seg[data-active] {
    filter: brightness(1.25);
  }
  .boot__legend {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 1px;
    background: hsl(var(--tn-fg-gutter) / 0.25);
    border: 1px solid hsl(var(--tn-fg-gutter) / 0.3);
    border-radius: 0.6rem;
    overflow: hidden;
  }
  .boot__row {
    display: grid;
    grid-template-columns: auto 1fr auto;
    align-items: center;
    column-gap: 0.7rem;
    padding: 0.5rem 0.8rem;
    background: hsl(var(--tn-bg-highlight) / 0.6);
    transition: background 0.18s ease;
  }
  .boot__row[data-active] {
    background: hsl(var(--tn-blue) / 0.12);
  }
  .boot__chip {
    width: 0.7rem;
    height: 0.7rem;
    border-radius: 3px;
  }
  .boot__row-label {
    font-size: 0.86rem;
    font-weight: 600;
    color: hsl(var(--tn-fg));
  }
  .boot__row-hint {
    grid-column: 2;
    font-size: 0.74rem;
    color: hsl(var(--tn-fg-dark));
  }
  .boot__row-ms {
    font-size: 0.82rem;
    font-variant-numeric: tabular-nums;
    color: hsl(var(--tn-cyan));
  }
  @media (max-width: 640px) {
    .boot__row {
      grid-template-columns: auto 1fr;
    }
    .boot__row-ms {
      grid-column: 2;
      justify-self: end;
    }
  }
</style>
