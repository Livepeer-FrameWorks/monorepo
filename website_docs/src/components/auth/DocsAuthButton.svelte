<script lang="ts">
  import { onMount } from "svelte";
  import {
    checkAuth,
    login,
    register,
    logout,
    type AuthUser,
    type BotProtectionData,
  } from "../../lib/auth";
  import DocsAuthPanel from "./DocsAuthPanel.svelte";

  let user = $state<AuthUser | null>(null);
  let loading = $state(true);
  let showPanel = $state(false);

  function handleAuthOpen() {
    if (!user) {
      // Delay one frame so the click-outside handler doesn't immediately close
      // the panel on the same event that triggered docs-auth-open.
      requestAnimationFrame(() => {
        showPanel = true;
      });
    }
  }

  onMount(async () => {
    user = await checkAuth();
    loading = false;
    dispatchAuthEvent();
    window.addEventListener("docs-auth-open", handleAuthOpen);
    return () => window.removeEventListener("docs-auth-open", handleAuthOpen);
  });

  function dispatchAuthEvent() {
    window.dispatchEvent(new CustomEvent("docs-auth-change", { detail: { user } }));
  }

  async function handleLogin(
    email: string,
    password: string,
    botProtection: BotProtectionData
  ): Promise<string | null> {
    const result = await login(email, password, botProtection);
    if (result.error) return result.error;
    user = result.user ?? null;
    showPanel = false;
    dispatchAuthEvent();
    return null;
  }

  async function handleRegister(
    email: string,
    password: string,
    displayName: string,
    botProtection: BotProtectionData
  ): Promise<string | null> {
    const result = await register(email, password, displayName, botProtection);
    if (result.error) return result.error;
    user = result.user ?? null;
    showPanel = false;
    dispatchAuthEvent();
    return null;
  }

  async function handleLogout() {
    await logout();
    user = null;
    showPanel = false;
    dispatchAuthEvent();
  }

  function handleClickOutside(event: MouseEvent) {
    const target = event.target as HTMLElement;
    if (showPanel && !target.closest(".docs-auth-wrapper")) {
      showPanel = false;
    }
  }
</script>

<svelte:window onclick={handleClickOutside} />

<div class="docs-auth-wrapper">
  {#if loading}
    <div class="docs-auth-btn docs-auth-btn--loading">
      <span class="docs-auth-spinner"></span>
    </div>
  {:else if user}
    <button
      class="docs-auth-btn docs-auth-btn--user"
      onclick={() => {
        showPanel = !showPanel;
      }}
      aria-label="Account menu"
    >
      <span class="docs-auth-avatar">
        {user.display_name?.[0]?.toUpperCase() ?? user.email[0].toUpperCase()}
      </span>
    </button>
  {:else}
    <button
      class="docs-auth-btn"
      onclick={() => {
        showPanel = !showPanel;
      }}
    >
      Sign in
    </button>
  {/if}

  {#if showPanel}
    <div class="docs-auth-dropdown">
      {#if user}
        <div class="docs-auth-dropdown__user">
          <div class="docs-auth-dropdown__name">{user.display_name || user.email}</div>
          <div class="docs-auth-dropdown__email">{user.email}</div>
        </div>
        <button class="docs-auth-dropdown__action" onclick={handleLogout}> Sign out </button>
      {:else}
        <DocsAuthPanel onLogin={handleLogin} onRegister={handleRegister} />
      {/if}
    </div>
  {/if}
</div>
