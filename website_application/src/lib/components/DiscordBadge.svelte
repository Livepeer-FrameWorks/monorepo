<script lang="ts">
  import { DISCORD_GUILD_ID, DISCORD_URL } from "@frameworks/site-config";

  const WIDGET_API = `https://discord.com/api/guilds/${DISCORD_GUILD_ID}/widget.json`;

  let inviteUrl = $state(DISCORD_URL);
  let presenceCount: number | null = $state(null);

  $effect(() => {
    if (!DISCORD_GUILD_ID) return;
    let cancelled = false;
    fetch(WIDGET_API)
      .then((r) => (r.ok ? r.json() : null))
      .then((data) => {
        if (cancelled || !data) return;
        if (data.instant_invite) inviteUrl = data.instant_invite;
        presenceCount = data.presence_count ?? null;
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  });
</script>

{#if inviteUrl}
  <!-- eslint-disable svelte/no-navigation-without-resolve -->
  <a
    href={inviteUrl}
    target="_blank"
    rel="noopener noreferrer"
    class="discord-badge"
    aria-label="Discord"
  >
    <svg viewBox="0 0 24 24" fill="currentColor" class="discord-badge__icon">
      <path
        d="M20.317 4.37a19.791 19.791 0 00-4.885-1.515.074.074 0 00-.079.037c-.21.375-.444.864-.608 1.25a18.27 18.27 0 00-5.487 0 12.64 12.64 0 00-.617-1.25.077.077 0 00-.079-.037A19.736 19.736 0 003.677 4.37a.07.07 0 00-.032.027C.533 9.046-.32 13.58.099 18.057a.082.082 0 00.031.057 19.9 19.9 0 005.993 3.03.078.078 0 00.084-.028c.462-.63.874-1.295 1.226-1.994a.076.076 0 00-.041-.106 13.107 13.107 0 01-1.872-.892.077.077 0 01-.008-.128 10.2 10.2 0 00.372-.292.074.074 0 01.077-.01c3.928 1.793 8.18 1.793 12.062 0a.074.074 0 01.078.01c.12.098.246.198.373.292a.077.077 0 01-.006.127 12.299 12.299 0 01-1.873.892.077.077 0 00-.041.107c.36.698.772 1.362 1.225 1.993a.076.076 0 00.084.028 19.839 19.839 0 006.002-3.03.077.077 0 00.032-.054c.5-5.177-.838-9.674-3.549-13.66a.061.061 0 00-.031-.03zM8.02 15.33c-1.183 0-2.157-1.085-2.157-2.419 0-1.333.956-2.419 2.157-2.419 1.21 0 2.176 1.096 2.157 2.42 0 1.333-.956 2.418-2.157 2.418zm7.975 0c-1.183 0-2.157-1.085-2.157-2.419 0-1.333.956-2.419 2.157-2.419 1.21 0 2.176 1.096 2.157 2.42 0 1.333-.947 2.418-2.157 2.418z"
      />
    </svg>
    {#if presenceCount != null}
      <span class="discord-badge__count">{presenceCount} online</span>
    {/if}
  </a>
  <!-- eslint-enable svelte/no-navigation-without-resolve -->
{/if}

<style>
  .discord-badge {
    display: inline-flex;
    align-items: center;
    gap: 0.375rem;
    color: var(--muted-foreground, hsl(var(--muted-foreground)));
    text-decoration: none;
    font-size: 0.875rem;
    transition: color 0.2s;
  }
  .discord-badge:hover {
    color: var(--foreground, hsl(var(--foreground)));
  }
  .discord-badge__icon {
    width: 1.25rem;
    height: 1.25rem;
  }
  .discord-badge__count {
    font-size: 0.75rem;
    font-variant-numeric: tabular-nums;
  }
</style>
