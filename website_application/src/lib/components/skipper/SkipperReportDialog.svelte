<script lang="ts">
  import { Dialog, DialogContent, DialogTitle, DialogDescription } from "$lib/components/ui/dialog";
  import { Button } from "$lib/components/ui/button";
  import { Badge } from "$lib/components/ui/badge";
  import { getIconComponent } from "$lib/iconUtils";
  import { notificationStore, type SkipperReport } from "$lib/stores/notifications.svelte";
  import { formatRelativeTime } from "$lib/utils/formatters";

  interface Props {
    open: boolean;
    reportId: string | null;
    onAskAbout?: (report: SkipperReport) => void;
    onOpenChange?: (value: boolean) => void;
  }

  let { open = $bindable(false), reportId, onAskAbout, onOpenChange }: Props = $props();

  let report = $state<SkipperReport | null>(null);
  let loading = $state(false);
  let notFound = $state(false);

  const AlertTriangleIcon = getIconComponent("AlertTriangle");
  const MessageSquareIcon = getIconComponent("MessageSquare");

  const confidenceLabels: Record<string, string> = {
    verified: "Verified",
    sourced: "Sourced",
    best_guess: "Best guess",
    unknown: "Unverified",
  };

  function confidenceClass(level: string): string {
    switch (level) {
      case "verified":
        return "border-[hsl(var(--tn-green)/0.4)] bg-[hsl(var(--tn-green)/0.1)] text-[hsl(var(--tn-green))]";
      case "sourced":
        return "border-primary/40 bg-primary/10 text-primary";
      case "best_guess":
        return "border-warning/40 bg-warning/10 text-warning";
      case "unknown":
      default:
        return "border-muted-foreground/30 bg-muted/40 text-muted-foreground";
    }
  }

  let lastLoadedId: string | null = null;

  $effect(() => {
    if (!open || !reportId) {
      if (!open) {
        report = null;
        notFound = false;
        lastLoadedId = null;
      }
      return;
    }
    if (reportId === lastLoadedId) return;
    lastLoadedId = reportId;
    loading = true;
    notFound = false;
    report = null;
    void (async () => {
      const r = await notificationStore.getReport(reportId);
      if (lastLoadedId !== reportId) return;
      loading = false;
      if (!r) {
        notFound = true;
        return;
      }
      report = r;
      if (!r.readAt) {
        void notificationStore.markRead([r.id]);
      }
    })();
  });

  function close() {
    open = false;
  }

  function handleAskAbout() {
    if (report && onAskAbout) {
      onAskAbout(report);
    }
    close();
  }
</script>

<Dialog
  {open}
  onOpenChange={(value) => {
    open = value;
    onOpenChange?.(value);
  }}
>
  <DialogContent
    class="max-w-2xl p-0 gap-0 border-none bg-transparent shadow-none overflow-hidden focus:outline-none"
  >
    <div class="slab w-full border border-border/50 shadow-xl">
      <div class="slab-header">
        <div class="flex items-center gap-3">
          <div
            class="flex h-8 w-8 items-center justify-center rounded bg-[hsl(var(--tn-red)/0.15)]"
          >
            <AlertTriangleIcon class="h-4 w-4 text-[hsl(var(--tn-red))]" />
          </div>
          <DialogTitle class="text-base font-bold text-foreground">
            {#if loading}
              Loading investigation…
            {:else if notFound}
              Report not found
            {:else}
              Investigation
            {/if}
          </DialogTitle>
          {#if report?.trigger}
            <Badge
              variant="outline"
              class="ml-auto uppercase tracking-wide text-[10px] h-5 bg-background/50"
            >
              {report.trigger}
            </Badge>
          {/if}
        </div>
      </div>

      <div class="slab-body--padded space-y-5 max-h-[70vh] overflow-y-auto">
        {#if loading}
          <div class="space-y-3">
            <div class="h-4 w-3/4 animate-pulse rounded bg-muted"></div>
            <div class="h-4 w-1/2 animate-pulse rounded bg-muted"></div>
            <div class="h-20 w-full animate-pulse rounded bg-muted"></div>
          </div>
        {:else if notFound}
          <DialogDescription class="text-sm text-muted-foreground">
            This investigation report no longer exists or you don't have access to it.
          </DialogDescription>
        {:else if report}
          <DialogDescription class="text-sm text-muted-foreground">
            {formatRelativeTime(report.createdAt)} · {new Date(report.createdAt).toLocaleString()}
          </DialogDescription>

          <section class="space-y-2">
            <h3 class="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
              Summary
            </h3>
            <p class="text-sm text-foreground leading-relaxed whitespace-pre-wrap">
              {report.summary}
            </p>
          </section>

          {#if report.rootCause}
            <section class="space-y-2">
              <h3 class="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
                Root cause
              </h3>
              <p class="text-sm text-foreground leading-relaxed whitespace-pre-wrap">
                {report.rootCause}
              </p>
            </section>
          {/if}

          {#if report.metricsReviewed.length > 0}
            <section class="space-y-2">
              <h3 class="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
                Metrics reviewed
              </h3>
              <div class="flex flex-wrap gap-1.5">
                {#each report.metricsReviewed as metric (metric)}
                  <span
                    class="inline-flex items-center rounded border border-border bg-muted/40 px-2 py-0.5 text-[11px] font-mono text-muted-foreground"
                  >
                    {metric}
                  </span>
                {/each}
              </div>
            </section>
          {/if}

          {#if report.recommendations.length > 0}
            <section class="space-y-2">
              <h3 class="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
                Recommendations
              </h3>
              <ul class="space-y-2">
                {#each report.recommendations as rec, i (i)}
                  <li class="flex items-start gap-2 rounded-md border border-border bg-card p-3">
                    <span
                      class="shrink-0 inline-flex items-center rounded-full border px-2 py-0.5 text-[10px] uppercase tracking-[0.16em] {confidenceClass(
                        rec.confidence
                      )}"
                    >
                      {confidenceLabels[rec.confidence] ?? rec.confidence}
                    </span>
                    <span class="text-sm text-foreground leading-relaxed">{rec.text}</span>
                  </li>
                {/each}
              </ul>
            </section>
          {/if}
        {/if}
      </div>

      <div class="slab-actions slab-actions--row flex gap-0">
        <Button
          variant="ghost"
          onclick={close}
          class="rounded-none h-12 flex-1 border-r border-[hsl(var(--tn-fg-gutter)/0.3)] hover:bg-muted/10 text-muted-foreground hover:text-foreground"
        >
          Close
        </Button>
        {#if report && onAskAbout}
          <Button
            variant="ghost"
            onclick={handleAskAbout}
            class="rounded-none h-12 flex-1 hover:bg-primary/10 text-primary hover:text-primary flex items-center justify-center gap-2"
          >
            <MessageSquareIcon class="h-3.5 w-3.5" />
            Ask Skipper about this
          </Button>
        {/if}
      </div>
    </div>
  </DialogContent>
</Dialog>
