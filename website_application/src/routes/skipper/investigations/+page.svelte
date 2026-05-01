<script lang="ts">
  import { onDestroy, onMount } from "svelte";
  import { resolve } from "$app/paths";
  import { auth } from "$lib/stores/auth";
  import { getIconComponent } from "$lib/iconUtils";
  import { notificationStore, type SkipperReport } from "$lib/stores/notifications.svelte";
  import { formatRelativeTime } from "$lib/utils/formatters";

  const AlertTriangleIcon = getIconComponent("AlertTriangle");
  const ArrowLeftIcon = getIconComponent("ArrowLeft");

  let isAuthenticated = false;
  let pageLimit = $state(50);

  const unsubscribeAuth = auth.subscribe((state) => {
    isAuthenticated = state.isAuthenticated;
  });

  onMount(async () => {
    if (!isAuthenticated) {
      await auth.checkAuth();
    }
    await notificationStore.loadReports(pageLimit, 0);
  });

  onDestroy(() => {
    unsubscribeAuth();
  });

  async function loadMore() {
    pageLimit += 50;
    await notificationStore.loadReports(pageLimit, 0);
  }

  function isUnread(r: SkipperReport): boolean {
    return r.readAt === null || r.readAt === undefined;
  }

  function reportHref(id: string): string {
    return `${resolve("/skipper")}?report=${encodeURIComponent(id)}`;
  }
</script>

<svelte:head>
  <title>Investigations - Skipper - FrameWorks</title>
</svelte:head>

<div class="flex h-full flex-col overflow-hidden">
  <div
    class="flex shrink-0 items-center justify-between border-b border-[hsl(var(--tn-fg-gutter)/0.3)] bg-background px-4 py-3 sm:px-6"
  >
    <div class="flex items-center gap-3">
      <a
        href={resolve("/skipper")}
        class="rounded-md p-1.5 text-muted-foreground transition hover:bg-muted hover:text-foreground"
        aria-label="Back to Skipper"
      >
        <ArrowLeftIcon class="h-4 w-4" />
      </a>
      <AlertTriangleIcon class="h-5 w-5 text-[hsl(var(--tn-red))]" />
      <div>
        <h1 class="text-lg font-bold text-foreground">Investigations</h1>
        <p class="text-xs text-muted-foreground">Skipper anomaly reports</p>
      </div>
      {#if notificationStore.unreadCount > 0}
        <span
          class="ml-2 inline-flex items-center justify-center min-w-[20px] h-5 px-1.5 text-[11px] font-bold leading-none text-white bg-[hsl(var(--tn-red))] rounded-full"
        >
          {notificationStore.unreadCount}
        </span>
      {/if}
    </div>
    {#if notificationStore.unreadCount > 0}
      <button
        onclick={() => notificationStore.markAllRead()}
        class="text-xs text-[hsl(var(--tn-blue))] hover:underline cursor-pointer"
      >
        Mark all read
      </button>
    {/if}
  </div>

  <div class="flex-1 overflow-y-auto bg-background/50">
    {#if notificationStore.loading && !notificationStore.initialized}
      <div class="space-y-2 p-4 sm:p-6">
        {#each Array.from({ length: 6 }) as _, i (i)}
          <div class="h-16 animate-pulse rounded bg-muted"></div>
        {/each}
      </div>
    {:else if notificationStore.reports.length === 0}
      <div class="flex h-full items-center justify-center p-8">
        <div class="text-center">
          <AlertTriangleIcon class="mx-auto h-8 w-8 text-muted-foreground/50" />
          <p class="mt-3 text-sm text-muted-foreground">No investigation reports yet.</p>
          <p class="mt-1 text-xs text-muted-foreground/60">
            Skipper will surface anomaly reports here as it detects them.
          </p>
        </div>
      </div>
    {:else}
      <ul class="mx-auto max-w-3xl divide-y divide-[hsl(var(--tn-fg-gutter)/0.15)]">
        {#each notificationStore.reports as report (report.id)}
          <li>
            <!-- eslint-disable svelte/no-navigation-without-resolve -->
            <a
              href={reportHref(report.id)}
              class="block px-4 py-3 sm:px-6 transition-colors hover:bg-[hsl(var(--tn-bg-visual))] {isUnread(
                report
              )
                ? 'border-l-2 border-l-[hsl(var(--tn-blue))]'
                : 'border-l-2 border-l-transparent'}"
            >
              <div class="flex items-start gap-3">
                <div class="flex-1 min-w-0">
                  <p
                    class="text-sm leading-snug {isUnread(report)
                      ? 'text-foreground font-medium'
                      : 'text-muted-foreground'}"
                  >
                    {report.summary}
                  </p>
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
                    {#if report.recommendations.length > 0}
                      <span class="text-[10px] text-muted-foreground">
                        · {report.recommendations.length} recommendation{report.recommendations
                          .length === 1
                          ? ""
                          : "s"}
                      </span>
                    {/if}
                  </div>
                </div>
              </div>
            </a>
            <!-- eslint-enable svelte/no-navigation-without-resolve -->
          </li>
        {/each}
      </ul>

      {#if notificationStore.totalCount > notificationStore.reports.length}
        <div class="flex justify-center p-4">
          <button
            onclick={loadMore}
            disabled={notificationStore.loading}
            class="text-xs text-[hsl(var(--tn-blue))] hover:underline disabled:opacity-50"
          >
            {notificationStore.loading ? "Loading…" : "Load more"}
          </button>
        </div>
      {/if}
    {/if}
  </div>
</div>
