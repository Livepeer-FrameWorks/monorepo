<script>
  // @ts-check
  import { page } from "$app/stores";
  import { goto } from "$app/navigation";
  import { navigationConfig } from "../navigation.js";
  import { createEventDispatcher } from "svelte";

  const dispatch = createEventDispatcher();

  // Start with all sections collapsed, expand only the current section
  let expandedSections = new Set();
  let hasInitialized = false;

  $: currentPath = $page.url.pathname;

  // Auto-expand the section containing the current page (only on initial load)
  $: {
    if (!hasInitialized) {
      const newExpandedSections = new Set();

      // Find which section contains the current path
      for (const [sectionKey, section] of Object.entries(navigationConfig)) {
        if (sectionKey !== "dashboard" && section.children) {
          for (const [childKey, child] of Object.entries(section.children)) {
            if (child.href === currentPath) {
              newExpandedSections.add(sectionKey);
              break;
            }
          }
        }
      }

      expandedSections = newExpandedSections;
      hasInitialized = true;
    }
  }

  /**
   * @param {string} sectionKey
   */
  function toggleSection(sectionKey) {
    if (expandedSections.has(sectionKey)) {
      expandedSections.delete(sectionKey);
    } else {
      expandedSections.add(sectionKey);
    }
    expandedSections = expandedSections; // Trigger reactivity
  }

  /**
   * @param {any} item
   */
  function handleNavigation(item) {
    if (item.active === "soon") {
      dispatch("comingSoon", { item });
      return;
    }
    if (item.active === "disabled") {
      dispatch("disabled", { item });
      return;
    }
    if (item.external) {
      // Handle external links - use actual contact page
      if (item.name === "Community") {
        window.open("https://frameworks.network/contact", "_blank");
      }
      return;
    }
    // Navigate to active routes using SvelteKit client-side routing
    if (item.href && item.active === true) {
      goto(item.href);
    }
  }

  /**
   * @param {any} item
   * @param {boolean} isChild
   * @param {string} currentPath
   */
  function getItemClass(item, isChild = false, currentPath = "") {
    const baseClass = "nav-item";
    const childPadding = isChild ? "pl-8" : "";

    if (item.active === "soon") {
      return `${baseClass} coming-soon ${childPadding}`;
    }
    if (item.active === "disabled") {
      return `${baseClass} disabled ${childPadding}`;
    }
    if (item.href === currentPath) {
      return `${baseClass} active ${childPadding}`;
    }
    return `${baseClass} ${childPadding}`;
  }
</script>

<div
  class="w-64 bg-tokyo-night-bg-light border-r border-tokyo-night-fg-gutter h-full overflow-y-auto"
>
  <!-- Navigation -->
  <nav class="p-4 space-y-2">
    <!-- Dashboard (always visible) -->
    <div class="mb-6">
      <button
        on:click={() => handleNavigation(navigationConfig.dashboard)}
        class="{getItemClass(
          navigationConfig.dashboard,
          false,
          currentPath
        )} w-full"
      >
        <span class="text-lg mr-3">{navigationConfig.dashboard.icon}</span>
        <span class="flex-1 text-left">{navigationConfig.dashboard.name}</span>
        {#if navigationConfig.dashboard.href === currentPath}
          <div class="w-2 h-2 bg-tokyo-night-blue rounded-full" />
        {/if}
      </button>
    </div>

    <!-- Feature Sections -->
    {#each Object.entries(navigationConfig) as [sectionKey, section]}
      {#if sectionKey !== "dashboard" && section.children}
        <div class="mb-4">
          <!-- Section Header -->
          <button
            on:click={() => toggleSection(sectionKey)}
            class="w-full flex items-center justify-between p-2 text-tokyo-night-fg-dark hover:text-tokyo-night-fg hover:bg-tokyo-night-bg-dark/50 rounded-lg transition-all duration-200"
          >
            <div class="flex items-center space-x-3">
              <span class="text-lg">{section.icon}</span>
              <span class="font-medium">{section.name}</span>
            </div>
            <svg
              class="w-4 h-4 transform transition-transform duration-200 {expandedSections.has(
                sectionKey
              )
                ? 'rotate-90'
                : ''}"
              fill="none"
              stroke="currentColor"
              viewBox="0 0 24 24"
            >
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                stroke-width="2"
                d="M9 5l7 7-7 7"
              />
            </svg>
          </button>

          <!-- Section Items -->
          {#if expandedSections.has(sectionKey)}
            <div class="mt-2 space-y-1">
              {#each Object.entries(section.children) as [childKey, child]}
                <button
                  on:click={() => handleNavigation(child)}
                  class="{getItemClass(child, true, currentPath)} w-full"
                >
                  <span class="text-sm mr-3">{child.icon}</span>
                  <span class="flex-1 text-left text-sm">{child.name}</span>

                  <!-- Badges -->
                  {#if child.badge}
                    <span class="badge badge-primary text-xs"
                      >{child.badge}</span
                    >
                  {:else if child.active === "soon"}
                    <span class="badge badge-warning text-xs">Soon</span>
                  {:else if child.tier}
                    <span class="badge badge-danger text-xs">{child.tier}</span>
                  {/if}

                  <!-- Active indicator -->
                  {#if child.href === currentPath}
                    <div
                      class="w-2 h-2 bg-tokyo-night-blue rounded-full ml-2"
                    />
                  {/if}
                </button>
              {/each}
            </div>
          {/if}
        </div>
      {/if}
    {/each}
  </nav>

  <!-- Footer -->
  <div class="p-4 border-t border-tokyo-night-fg-gutter mt-auto">
    <div class="text-xs text-tokyo-night-comment">
      <p class="mb-2">FrameWorks v0.0.1</p>
      <p>Powered by Livepeer & MistServer</p>
    </div>
  </div>
</div>
