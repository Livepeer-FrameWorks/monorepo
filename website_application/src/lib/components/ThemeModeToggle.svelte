<script lang="ts">
  import { themeStore } from "$lib/stores/theme.svelte";
</script>

{#if themeStore.hasMultipleModes}
  <button
    onclick={() => themeStore.toggleMode()}
    class="flex items-center justify-center px-4 border-l border-[hsl(var(--tn-fg-gutter)/0.3)] text-muted-foreground hover:text-foreground hover:bg-[hsl(var(--tn-bg-visual))] transition-colors cursor-pointer"
    title={themeStore.isDark ? "Switch to light mode" : "Switch to dark mode"}
    aria-label={themeStore.isDark ? "Switch to light mode" : "Switch to dark mode"}
  >
    {#if themeStore.isDark}
      <svg class="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
        <path
          stroke-linecap="round"
          stroke-linejoin="round"
          stroke-width="2"
          d="M12 3v1m0 16v1m9-9h-1M4 12H3m15.364 6.364l-.707-.707M6.343 6.343l-.707-.707m12.728 0l-.707.707M6.343 17.657l-.707.707M16 12a4 4 0 11-8 0 4 4 0 018 0z"
        />
      </svg>
    {:else}
      <svg class="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
        <path
          stroke-linecap="round"
          stroke-linejoin="round"
          stroke-width="2"
          d="M20.354 15.354A9 9 0 018.646 3.646 9.003 9.003 0 0012 21a9.003 9.003 0 008.354-5.646z"
        />
      </svg>
    {/if}
  </button>
{/if}
