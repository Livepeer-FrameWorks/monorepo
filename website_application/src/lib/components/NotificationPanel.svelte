<script lang="ts">
  import { notificationStore, type SkipperReport } from "$lib/stores/notifications.svelte";
  import { resolve } from "$app/paths";
  import { onMount } from "svelte";

  const skipperPath = resolve("/skipper");

  let panelEl: HTMLDivElement | undefined = $state();

  onMount(() => {
    function handleClickOutside(e: MouseEvent) {
      if (panelEl && !panelEl.contains(e.target as Node)) {
        notificationStore.closePanel();
      }
    }
    document.addEventListener("click", handleClickOutside, true);
    return () => document.removeEventListener("click", handleClickOutside, true);
  });

  function formatRelativeTime(dateStr: string): string {
    const now = Date.now();
    const then = new Date(dateStr).getTime();
    const diffMs = now - then;
    const diffMin = Math.floor(diffMs / 60000);
    if (diffMin < 1) return "just now";
    if (diffMin < 60) return `${diffMin}m ago`;
    const diffHr = Math.floor(diffMin / 60);
    if (diffHr < 24) return `${diffHr}h ago`;
    const diffDay = Math.floor(diffHr / 24);
    return `${diffDay}d ago`;
  }

  function isUnread(report: SkipperReport): boolean {
    return report.readAt === null || report.readAt === undefined;
  }

  function handleMarkAllRead() {
    notificationStore.markAllRead();
  }
</script>

<div
  bind:this={panelEl}
  class="absolute top-full right-0 z-50 w-96 max-h-[28rem] flex flex-col bg-background border border-[hsl(var(--tn-fg-gutter)/0.3)] shadow-lg"
>
  <!-- Header -->
  <div
    class="flex items-center justify-between px-4 py-3 border-b border-[hsl(var(--tn-fg-gutter)/0.3)]"
  >
    <span class="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
      Notifications
    </span>
    {#if notificationStore.unreadCount > 0}
      <span class="text-xs text-muted-foreground">
        {notificationStore.unreadCount} unread
      </span>
    {/if}
  </div>

  <!-- Body -->
  <div class="flex-1 overflow-y-auto">
    {#if notificationStore.loading && !notificationStore.initialized}
      <div class="p-6 text-center text-sm text-muted-foreground">Loading...</div>
    {:else if notificationStore.reports.length === 0}
      <div class="p-6 text-center text-sm text-muted-foreground">No investigation reports yet</div>
    {:else}
      {#each notificationStore.reports as report (report.id)}
        <a
          href={skipperPath}
          onclick={() => notificationStore.closePanel()}
          class="block px-4 py-3 border-b border-[hsl(var(--tn-fg-gutter)/0.1)] hover:bg-[hsl(var(--tn-bg-visual))] transition-colors {isUnread(
            report
          )
            ? 'border-l-2 border-l-[hsl(var(--tn-blue))]'
            : 'border-l-2 border-l-transparent'}"
        >
          <div class="flex items-start justify-between gap-2">
            <p
              class="text-sm leading-snug line-clamp-2 {isUnread(report)
                ? 'text-foreground font-medium'
                : 'text-muted-foreground'}"
            >
              {report.summary}
            </p>
          </div>
          <div class="mt-1 flex items-center gap-2">
            {#if report.trigger}
              <span
                class="text-[10px] uppercase tracking-wider px-1.5 py-0.5 rounded bg-[hsl(var(--tn-bg-visual))] text-muted-foreground"
              >
                {report.trigger}
              </span>
            {/if}
            <span class="text-[10px] text-muted-foreground">
              {formatRelativeTime(report.createdAt)}
            </span>
          </div>
        </a>
      {/each}
    {/if}
  </div>

  <!-- Footer Actions -->
  {#if notificationStore.reports.length > 0}
    <div
      class="flex items-center justify-between px-4 py-2 border-t border-[hsl(var(--tn-fg-gutter)/0.3)]"
    >
      {#if notificationStore.unreadCount > 0}
        <button
          onclick={handleMarkAllRead}
          class="text-xs text-[hsl(var(--tn-blue))] hover:underline cursor-pointer"
        >
          Mark all read
        </button>
      {:else}
        <span class="text-xs text-muted-foreground">All caught up</span>
      {/if}
      <a
        href={skipperPath}
        onclick={() => notificationStore.closePanel()}
        class="text-xs text-[hsl(var(--tn-blue))] hover:underline"
      >
        View all
      </a>
    </div>
  {/if}
</div>
