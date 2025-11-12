<script>
  import { run } from "svelte/legacy";

  // @ts-check
  import { page } from "$app/stores";
  import { goto } from "$app/navigation";
  import { resolve } from "$app/paths";
  import { navigationConfig } from "../navigation.js";
  import { createEventDispatcher } from "svelte";
  import { getIconComponent } from "../iconUtils";

  /**
   * @typedef {Object} Props
   * @property {boolean} [collapsed]
   */

  /** @type {Props} */
  let { collapsed = false } = $props();

  const dispatch = createEventDispatcher();

  // Start with all sections collapsed, expand only the current section
  /** @type {Set<string>} */
  let expandedSections = $state(new Set());
  /** @type {Set<string>} */
  let manuallyExpanded = $state(new Set());
  /** @type {Set<string>} */
  let manuallyCollapsed = $state(new Set());

  // Track current child index for cycling when collapsed
  /** @type {Record<string, number>} */
  let sectionChildIndex = {};

  let currentPath = $derived($page.url.pathname);

  // Auto-expand the section containing the current page, but only if not manually collapsed
  run(() => {
    // eslint-disable-next-line svelte/prefer-svelte-reactivity
    const newExpandedSections = new Set(manuallyExpanded);

    // Find which section contains the current path
    for (const [sectionKey, section] of Object.entries(navigationConfig)) {
      if (sectionKey !== "dashboard" && section.children) {
        for (const [_childKey, child] of Object.entries(section.children)) {
          if (resolve(child.href) === currentPath) {
            // Only auto-expand if user hasn't manually collapsed this section
            if (!manuallyCollapsed.has(sectionKey)) {
              newExpandedSections.add(sectionKey);
            }
            break;
          }
        }
      }
    }

    expandedSections = newExpandedSections;
  });

  /**
   * @param {string} sectionKey
   */
  function toggleSection(sectionKey) {
    if (collapsed) {
      // When collapsed, cycle through the section's children
      cycleToNextChild(sectionKey);
    } else {
      // Normal toggle behavior when expanded
      const newExpanded = new Set(manuallyExpanded);
      const newCollapsed = new Set(manuallyCollapsed);

      if (expandedSections.has(sectionKey)) {
        // Currently expanded - collapse it and mark as manually collapsed
        newExpanded.delete(sectionKey);
        newCollapsed.add(sectionKey);
      } else {
        // Currently collapsed - expand it and remove from manually collapsed
        newCollapsed.delete(sectionKey);
        newExpanded.add(sectionKey);
      }

      manuallyExpanded = newExpanded;
      manuallyCollapsed = newCollapsed;
    }
  }

  /**
   * Cycle to the next child in a section when sidebar is collapsed
   * @param {string} sectionKey
   */
  function cycleToNextChild(sectionKey) {
    const section = navigationConfig[sectionKey];
    if (!section?.children) return;

    const childEntries = Object.entries(section.children);
    const activeChildren = childEntries.filter(
      ([_, child]) => child.active === true,
    );

    if (activeChildren.length === 0) return;

    // Initialize or get current index for this section
    if (!(sectionKey in sectionChildIndex)) {
      sectionChildIndex[sectionKey] = 0;
    }

    // Find current page index if we're already in this section
    const currentChildIndex = activeChildren.findIndex(
      ([, child]) => resolve(child.href) === currentPath,
    );

    if (currentChildIndex !== -1) {
      // We're in this section, go to next child
      sectionChildIndex[sectionKey] =
        (currentChildIndex + 1) % activeChildren.length;
    } else {
      // Not in this section, go to first child
      sectionChildIndex[sectionKey] = 0;
    }

    // Navigate to the selected child
    const targetEntry = activeChildren[sectionChildIndex[sectionKey]];
    if (targetEntry) {
      handleNavigation(targetEntry[1]);
    }
  }

  /**
   * @param {NavigationItem} item
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
      goto(resolve(item.href));
    }
  }

  /**
   * @param {NavigationItem} item
   * @param {boolean} [isChild]
   * @param {string} [currentPath]
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
    if (item.href && resolve(item.href) === currentPath) {
      return `${baseClass} active ${childPadding}`;
    }
    return `${baseClass} ${childPadding}`;
  }

  const SvelteComponent = $derived(
    getIconComponent(navigationConfig.dashboard.icon),
  );
</script>

<div
  class="{collapsed
    ? 'w-16'
    : 'w-64'} bg-tokyo-night-bg-light border-r border-tokyo-night-fg-gutter h-full overflow-y-auto transition-all duration-300 select-none"
>
  <!-- Navigation -->
  <nav class="{collapsed ? 'p-2' : 'p-4'} space-y-2">
    <!-- Dashboard (always visible) -->
    <div class="mb-6">
      <button
        onclick={() => handleNavigation(navigationConfig.dashboard)}
        class="{getItemClass(
          navigationConfig.dashboard,
          false,
          currentPath,
        )} w-full {collapsed ? 'justify-center' : ''}"
        title={collapsed ? navigationConfig.dashboard.name : ""}
      >
        <SvelteComponent class="w-5 h-5 flex-shrink-0 {collapsed ? '' : 'mr-3'}" />
        {#if !collapsed}
          <span class="flex-1 text-left">{navigationConfig.dashboard.name}</span
          >
          {#if resolve(navigationConfig.dashboard.href) === currentPath}
            <div class="w-2 h-2 bg-tokyo-night-blue rounded-full"></div>
          {/if}
        {:else if resolve(navigationConfig.dashboard.href) === currentPath}
          <div
            class="absolute right-1 w-2 h-2 bg-tokyo-night-blue rounded-full"
          ></div>
        {/if}
      </button>
    </div>

    <!-- Feature Sections -->
    {#each Object.entries(navigationConfig) as [sectionKey, section] (sectionKey)}
      {#if sectionKey !== "dashboard" && section.children}
        {@const SvelteComponent_1 = getIconComponent(section.icon)}
        <div class="mb-4">
          <!-- Section Header -->
          <button
            onclick={() => toggleSection(sectionKey)}
            class="nav-item w-full {collapsed
              ? 'justify-center'
              : 'justify-between'}"
            title={collapsed ? section.name : ""}
          >
            <div class="flex items-center {collapsed ? '' : 'space-x-3'}">
              <SvelteComponent_1 class="w-5 h-5" />
              {#if !collapsed}
                <span class="font-medium">{section.name}</span>
              {/if}
            </div>
            {#if !collapsed}
              <svg
                class="w-4 h-4 transform transition-transform duration-200 {expandedSections.has(
                  sectionKey,
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
            {/if}
          </button>

          <!-- Section Items -->
          {#if expandedSections.has(sectionKey) && !collapsed}
            <div class="mt-2 space-y-1">
              {#each Object.entries(section.children) as [_childKey, child] (_childKey)}
                {@const SvelteComponent_2 = getIconComponent(child.icon)}
                <button
                  onclick={() => handleNavigation(child)}
                  class="{getItemClass(child, true, currentPath)} w-full"
                >
                  <SvelteComponent_2 class="w-4 h-4 mr-3" />
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
                  {#if resolve(child.href) === currentPath}
                    <div
                      class="w-2 h-2 bg-tokyo-night-blue rounded-full ml-2"
                    ></div>
                  {/if}
                </button>
              {/each}
            </div>
          {/if}
        </div>
      {/if}
    {/each}
  </nav>
</div>
