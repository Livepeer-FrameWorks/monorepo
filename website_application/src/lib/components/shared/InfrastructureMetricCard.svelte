<script lang="ts">
  interface Props {
    label?: string;
    title?: string;
    subtitle?: string;
    value: string | number;
    tone?: string;
    class?: string;
  }

  let {
    label = "",
    title = "",
    subtitle = "",
    value,
    tone = "text-foreground",
    class: className = "",
  }: Props = $props();

  let displayTitle = $derived(title || label || "Metric");
</script>

<div class="metric-card h-full flex flex-col justify-between gap-3 {className}">
  <div class="min-w-0">
    <p
      class="text-[0.65rem] uppercase tracking-[0.08em] text-muted-foreground font-medium truncate"
      title={displayTitle}
    >
      {displayTitle}
    </p>
    {#if subtitle}
      <p class="mt-1 text-xs text-muted-foreground/80 leading-snug">
        {subtitle}
      </p>
    {/if}
  </div>
  <p class="text-2xl md:text-[1.65rem] leading-none font-semibold break-words {tone}">{value}</p>
</div>
