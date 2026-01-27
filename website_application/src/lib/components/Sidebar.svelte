<script lang="ts">
  import { page } from "$app/stores";
  import { goto } from "$app/navigation";
  import { base, resolve } from "$app/paths";
  import { navigationConfig, type NavigationItem } from "../navigation.js";
  import { createEventDispatcher, untrack } from "svelte";
  import { getIconComponent } from "../iconUtils";
  import { sidebarStore } from "../stores/sidebar.svelte";
  import { getMarketingSiteUrl } from "$lib/config";

  interface Props {
    collapsed?: boolean;
  }

  let { collapsed = false }: Props = $props();

  const dispatch = createEventDispatcher();
  const marketingSiteUrl = getMarketingSiteUrl().replace(/\/$/, "");

  // Track current child index for cycling when collapsed
  let sectionChildIndex: Record<string, number> = {};

  let currentPath = $derived($page.url.pathname);

  // Helper to resolve hrefs safely - prepend base path if configured
  function safeResolve(href: string | undefined): string {
    if (!href) return "";
    // Simply prepend base path (empty string by default, or configured base path)
    return base + href;
  }

  // Helper to get active children info for dot indicators in collapsed mode
  function getActiveChildrenInfo(sectionKey: string): {
    count: number;
    currentIndex: number;
    isCurrentSection: boolean;
  } {
    const section = navigationConfig[sectionKey];
    if (!section?.children) return { count: 0, currentIndex: -1, isCurrentSection: false };

    const activeChildren = Object.values(section.children).filter((child) => child.active === true);

    const currentIndex = activeChildren.findIndex(
      (child) => safeResolve(child.href) === currentPath
    );

    // Check if we're currently in this section (any child matches)
    const isCurrentSection = currentIndex !== -1;

    return { count: activeChildren.length, currentIndex, isCurrentSection };
  }

  // Check if a section has any active children
  function sectionHasActiveChildren(sectionKey: string): boolean {
    const section = navigationConfig[sectionKey];
    if (!section?.children) return false;
    return Object.values(section.children).some((child) => child.active === true);
  }

  // Helper to check if a section should appear expanded
  // Section is expanded if: user explicitly expanded it OR it contains the current route
  function isSectionExpanded(sectionKey: string): boolean {
    return sidebarStore.expandedSections.has(sectionKey);
  }

  // Auto-expand the section containing the current page on navigation
  $effect(() => {
    for (const [sectionKey, section] of Object.entries(navigationConfig)) {
      if (sectionKey !== "dashboard" && section.children) {
        for (const [_childKey, child] of Object.entries(section.children)) {
          if (safeResolve(child.href) === currentPath) {
            // Expand the section in the store (non-persisted) so user can toggle it later
            // Use untrack to prevent this effect from re-running if the store changes
            untrack(() => {
              sidebarStore.autoExpandSection(sectionKey);
            });
            break;
          }
        }
      }
    }
  });

  function toggleSection(sectionKey: string) {
    if (collapsed) {
      // When collapsed, cycle through the section's children
      cycleToNextChild(sectionKey);
    } else {
      // Normal toggle behavior when expanded - use the store
      sidebarStore.toggleSection(sectionKey);
    }
  }

  function cycleToNextChild(sectionKey: string) {
    const section = navigationConfig[sectionKey];
    if (!section?.children) return;

    const childEntries = Object.entries(section.children);
    const activeChildren = childEntries.filter(([_, child]) => child.active === true);

    if (activeChildren.length === 0) return;

    // Initialize or get current index for this section
    if (!(sectionKey in sectionChildIndex)) {
      sectionChildIndex[sectionKey] = 0;
    }

    // Find current page index if we're already in this section
    const currentChildIndex = activeChildren.findIndex(
      ([, child]) => safeResolve(child.href) === currentPath
    );

    if (currentChildIndex !== -1) {
      // We're in this section, go to next child
      sectionChildIndex[sectionKey] = (currentChildIndex + 1) % activeChildren.length;
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

  function handleNavigation(item: NavigationItem) {
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
        window.open(`${marketingSiteUrl}/contact`, "_blank");
      }
      return;
    }
    // Navigate to active routes using SvelteKit client-side routing
    if (item.href && item.active === true) {
      goto(resolve(safeResolve(item.href)));
    }
  }

  function getItemClass(item: NavigationItem, isChild = false, currentPath = "") {
    const baseClass = "nav-item";
    const childPadding = isChild ? "pl-8" : "";

    if (item.active === "soon") {
      return `${baseClass} coming-soon ${childPadding}`;
    }
    if (item.active === "disabled") {
      return `${baseClass} disabled ${childPadding}`;
    }
    if (item.href && safeResolve(item.href) === currentPath) {
      return `${baseClass} active ${childPadding}`;
    }
    return `${baseClass} ${childPadding}`;
  }

  const SvelteComponent = $derived(getIconComponent(navigationConfig.dashboard.icon));
</script>

<div
  class="{collapsed
    ? 'w-16'
    : 'w-64'} bg-sidebar border-r border-sidebar-border h-full overflow-y-auto transition-all duration-300 select-none"
>
  <!-- Navigation -->
  <nav class="{collapsed ? 'p-2' : 'p-4'} space-y-2">
    <!-- Dashboard (always visible) -->
    <div class="mb-6">
      <button
        onclick={() => handleNavigation(navigationConfig.dashboard)}
        class="{getItemClass(navigationConfig.dashboard, false, currentPath)} w-full {collapsed
          ? 'justify-center relative'
          : ''}"
        title={collapsed ? navigationConfig.dashboard.name : ""}
      >
        <SvelteComponent class="w-5 h-5 flex-shrink-0 {collapsed ? '' : 'mr-3'}" />
        {#if !collapsed}
          <span class="flex-1 text-left">{navigationConfig.dashboard.name}</span>
        {/if}
      </button>
    </div>

    <!-- Feature Sections -->
    {#each Object.entries(navigationConfig) as [sectionKey, section] (sectionKey)}
      {#if sectionKey !== "dashboard" && section.children}
        {@const SvelteComponent_1 = getIconComponent(section.icon)}
        {@const hasActiveChildren = sectionHasActiveChildren(sectionKey)}
        {@const childInfo = getActiveChildrenInfo(sectionKey)}
        <div class="mb-4">
          <!-- Section Header -->
          <button
            onclick={() => toggleSection(sectionKey)}
            class="nav-item w-full {collapsed
              ? 'flex-col items-center justify-center'
              : 'justify-between'} {!hasActiveChildren ? 'disabled' : ''} {collapsed &&
            childInfo.isCurrentSection
              ? 'active'
              : ''}"
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
                class="w-4 h-4 transform transition-transform duration-200 {isSectionExpanded(
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
            {:else}
              <!-- Dot indicators for collapsed mode -->
              {#if childInfo.count > 1}
                <div class="flex justify-center gap-1 mt-1.5">
                  {#each Array(childInfo.count) as _, i (i)}
                    <div
                      class="w-1.5 h-1.5 rounded-full transition-colors {i ===
                      childInfo.currentIndex
                        ? 'bg-primary'
                        : 'bg-muted-foreground/30'}"
                    ></div>
                  {/each}
                </div>
              {:else if childInfo.count === 1}
                <!-- Single active child - show one dot when active -->
                <div class="flex justify-center mt-1.5">
                  <div
                    class="w-1.5 h-1.5 rounded-full transition-colors {childInfo.isCurrentSection
                      ? 'bg-primary'
                      : 'bg-muted-foreground/30'}"
                  ></div>
                </div>
              {/if}
            {/if}
          </button>

          <!-- Section Items -->
          {#if isSectionExpanded(sectionKey) && !collapsed}
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
                    <span class="badge badge-primary text-xs">{child.badge}</span>
                  {:else if child.active === "soon"}
                    <span class="badge badge-warning text-xs">Soon</span>
                  {:else if child.tier}
                    <span class="badge badge-danger text-xs">{child.tier}</span>
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
