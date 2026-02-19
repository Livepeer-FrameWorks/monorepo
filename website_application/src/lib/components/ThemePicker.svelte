<script lang="ts">
  import { themeStore } from "$lib/stores/theme.svelte";
  import { THEME_PALETTES, THEME_IDS } from "$lib/themes/palettes";
</script>

<div class="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-5 gap-3">
  {#each THEME_IDS as id (id)}
    {@const theme = THEME_PALETTES[id]}
    {@const palette = theme.dark ?? theme.light!}
    {@const isActive = themeStore.themeId === id}
    <button
      onclick={() => themeStore.setTheme(id)}
      class="group relative flex flex-col items-center gap-2 p-3 border transition-all cursor-pointer
        {isActive
        ? 'border-[hsl(var(--tn-blue))] bg-[hsl(var(--tn-blue)/0.08)]'
        : 'border-[hsl(var(--tn-fg-gutter)/0.3)] hover:border-[hsl(var(--tn-fg-gutter)/0.6)]'}"
    >
      <!-- Color swatch preview -->
      <div class="w-full h-8 flex gap-0.5 overflow-hidden">
        <div class="flex-1" style="background-color: {palette.bg}"></div>
        <div class="flex-1" style="background-color: {palette.bgHighlight}"></div>
        <div class="flex-1" style="background-color: {palette.blue}"></div>
        <div class="flex-1" style="background-color: {palette.green}"></div>
        <div class="flex-1" style="background-color: {palette.purple}"></div>
      </div>

      <!-- Theme name -->
      <span class="text-xs font-medium {isActive ? 'text-foreground' : 'text-muted-foreground'}">
        {theme.name}
      </span>

      <!-- Mode indicators -->
      <div class="flex gap-1">
        {#if theme.dark}
          <span
            class="w-2 h-2 rounded-full border border-[hsl(var(--tn-fg-gutter)/0.4)]"
            style="background-color: {palette.bgDark}"
            title="Dark mode"
          ></span>
        {/if}
        {#if theme.light}
          <span
            class="w-2 h-2 rounded-full border border-[hsl(var(--tn-fg-gutter)/0.4)]"
            style="background-color: #f0f0f0"
            title="Light mode"
          ></span>
        {/if}
      </div>

      <!-- Active checkmark -->
      {#if isActive}
        <div class="absolute top-1.5 right-1.5">
          <svg
            class="w-4 h-4 text-[hsl(var(--tn-blue))]"
            fill="none"
            stroke="currentColor"
            viewBox="0 0 24 24"
          >
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              stroke-width="2.5"
              d="M5 13l4 4L19 7"
            />
          </svg>
        </div>
      {/if}
    </button>
  {/each}
</div>
