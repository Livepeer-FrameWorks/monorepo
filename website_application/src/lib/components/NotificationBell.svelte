<script lang="ts">
  import { Bell } from "$lib/iconUtils";
  import { notificationStore } from "$lib/stores/notifications.svelte";
  import { onMount } from "svelte";
  import { auth } from "$lib/stores/auth";
  import NotificationPanel from "./NotificationPanel.svelte";

  let isAuthenticated = $state(false);

  auth.subscribe((s) => {
    isAuthenticated = s.isAuthenticated;
  });

  onMount(() => {
    if (isAuthenticated) {
      notificationStore.loadReports();
    }
  });

  function handleClick(e: MouseEvent) {
    e.stopPropagation();
    notificationStore.togglePanel();
  }
</script>

<div class="relative flex items-stretch">
  <button
    onclick={handleClick}
    class="flex items-center justify-center px-4 border-l border-[hsl(var(--tn-fg-gutter)/0.3)] text-muted-foreground hover:text-foreground hover:bg-[hsl(var(--tn-bg-visual))] transition-colors cursor-pointer"
    title="Notifications"
  >
    <svelte:component this={Bell} class="w-5 h-5" />
    {#if notificationStore.unreadCount > 0}
      <span
        class="absolute top-2 right-2 flex items-center justify-center min-w-[18px] h-[18px] px-1 text-[10px] font-bold leading-none text-white bg-[hsl(var(--tn-red))] rounded-full"
      >
        {notificationStore.unreadCount > 9 ? "9+" : notificationStore.unreadCount}
      </span>
    {/if}
  </button>

  {#if notificationStore.panelOpen}
    <NotificationPanel />
  {/if}
</div>
