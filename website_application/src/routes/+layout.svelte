<script>
  import "../app.css";
  import { onMount } from "svelte";
  import { goto } from "$app/navigation";
  import { page } from "$app/stores";
  import { base } from "$app/paths";
  import { auth } from "$lib/stores/auth";
  import { getMarketingSiteUrl } from "$lib/config";
  import { getRouteInfo } from "$lib/navigation.js";
  import Sidebar from "$lib/components/Sidebar.svelte";
  import ComingSoonModal from "$lib/components/ComingSoonModal.svelte";
  import Toast from "$lib/components/Toast.svelte";
  import ErrorBoundary from "$lib/components/ErrorBoundary.svelte";
  import BetaBadge from "$lib/components/BetaBadge.svelte";

  let isAuthenticated = false;
  /** @type {any} */
  let user = null;
  let loading = true;
  let initialized = false;
  let mobileMenuOpen = false;
  let sidebarCollapsed = false;

  // Coming Soon Modal state
  let showComingSoonModal = false;
  /** @type {any} */
  let selectedFeature = null;

  // Define public routes that don't require authentication
  const publicRoutes = [`${base}/login`, `${base}/login/`, `${base}/register`, `${base}/register/`];

  // Subscribe to auth store
  auth.subscribe((authState) => {
    isAuthenticated = authState.isAuthenticated;
    // Fix: authState.user contains the full API response: { user: {...}, streams: [...] }
    user = authState.user?.user || null;
    loading = authState.loading;
    initialized = authState.initialized;
  });

  // Get current page title from navigation config
  $: currentPageTitle = (() => {
    const currentPath = $page.url.pathname.replace(base, '') || '/';
    const routeInfo = getRouteInfo(currentPath);
    return routeInfo ? routeInfo.name : 'Page';
  })();

  // Reactive statement to handle route protection
  $: {
    // Only run route protection when not loading AND after initialization
    if (!loading && initialized) {
      const currentPath = $page.url.pathname;
      const isPublicRoute = publicRoutes.includes(currentPath);

      if (!isAuthenticated && !isPublicRoute) {
        // Redirect unauthenticated users to login
        goto(`${base}/login`);
      } else if (isAuthenticated && isPublicRoute) {
        // Redirect authenticated users away from auth pages
        goto(`${base}/`);
      }
    }
  }

  onMount(async () => {
    await auth.checkAuth();
  });

  function logout() {
    auth.logout();
    goto(`${base}/login`);
  }

  /**
   * @param {CustomEvent} event
   */
  function handleComingSoon(event) {
    selectedFeature = event.detail.item;
    showComingSoonModal = true;
  }

  function closeComingSoonModal() {
    showComingSoonModal = false;
    selectedFeature = null;
  }

  function toggleMobileMenu() {
    mobileMenuOpen = !mobileMenuOpen;
  }

  function toggleSidebar() {
    sidebarCollapsed = !sidebarCollapsed;
  }
</script>

{#if loading && !initialized}
  <!-- Loading Screen -->
  <div class="min-h-screen bg-tokyo-night-bg flex items-center justify-center">
    <div class="text-center">
      <div class="inline-flex items-center">
        <img
          src="/frameworks-dark-horizontal-lockup-transparent.svg"
          alt="FrameWorks"
          class="h-16 animate-pulse"
        />
      </div>
      <div class="mt-4 text-tokyo-night-comment">Loading...</div>
    </div>
  </div>
{:else}
  <div class="min-h-screen bg-tokyo-night-bg text-tokyo-night-fg">
    {#if isAuthenticated}
      <!-- Authenticated Layout with Sidebar -->
      <div class="flex flex-col h-screen">
        <!-- Top Navigation - Full Width -->
        <nav
          class="bg-tokyo-night-bg-light border-b border-tokyo-night-fg-gutter px-6 py-4"
        >
          <div class="flex justify-between items-center">
            <div class="flex items-center space-x-4">
              <!-- Sidebar Toggle -->
              <button
                on:click={toggleSidebar}
                class="p-2 rounded-lg text-tokyo-night-fg-dark hover:text-tokyo-night-fg hover:bg-tokyo-night-bg-dark/50 transition-colors"
                title="Toggle Sidebar"
              >
                <svg class="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                  {#if sidebarCollapsed}
                    <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 6h16M4 12h16M4 18h16" />
                  {:else}
                    <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M11 19l-7-7 7-7m8 14l-7-7 7-7" />
                  {/if}
                </svg>
              </button>
              
              <!-- FrameWorks Branding -->
              <a
                href="{base}/"
                class="flex items-center hover:opacity-80 transition-opacity"
              >
                <img
                  src="/frameworks-dark-horizontal-lockup-transparent.svg"
                  alt="FrameWorks"
                  class="h-10"
                />
              </a>
              
              <!-- Beta Badge -->
              <BetaBadge />

              <!-- Page Title -->
              <div class="text-tokyo-night-comment">•</div>
              <h1 class="text-lg font-semibold text-tokyo-night-fg">
                {currentPageTitle}
              </h1>
            </div>

            <div class="flex items-center space-x-4">
              <span class="text-tokyo-night-fg-dark"
                >Welcome, <span class="text-tokyo-night-blue"
                  >{user?.name || user?.email}</span
                ></span
              >
              <button on:click={logout} class="btn-secondary"> Logout </button>
            </div>
          </div>
        </nav>

        <!-- Main Content Area with Sidebar -->
        <div class="flex flex-1 overflow-hidden">
          <!-- Sidebar -->
          <Sidebar 
            on:comingSoon={handleComingSoon} 
            collapsed={sidebarCollapsed}
          />

          <!-- Page Content -->
          <main class="flex-1 overflow-y-auto bg-tokyo-night-bg p-6">
            <div class="max-w-7xl mx-auto">
              <slot />
            </div>
          </main>
        </div>
      </div>
    {:else}
      <!-- Unauthenticated Layout (Login/Register pages only) -->
      <div
        class="min-h-screen bg-gradient-to-br from-tokyo-night-bg via-tokyo-night-bg-dark to-tokyo-night-bg"
      >
        <!-- Marketing-style Navigation for auth pages -->
        <nav
          class="sticky top-0 z-50 bg-tokyo-night-bg/95 backdrop-blur-sm border-b border-tokyo-night-fg-gutter"
        >
          <div class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8">
            <div class="flex justify-between items-center h-16 lg:h-20">
              <!-- Logo Section -->
              <div class="flex items-center gap-3">
                <a
                  href={getMarketingSiteUrl()}
                  class="flex items-center hover:opacity-80 transition-opacity"
                >
                  <img
                    src="/frameworks-dark-horizontal-lockup-transparent.svg"
                    alt="FrameWorks"
                    class="h-10"
                  />
                </a>
                <BetaBadge />
              </div>

              <!-- Desktop Navigation -->
              <div class="hidden lg:flex items-center space-x-8">
                <!-- Navigation Links -->
                <div class="flex items-center space-x-8">
                  <a
                    href={getMarketingSiteUrl() + "/about"}
                    class="text-tokyo-night-fg-dark hover:text-tokyo-night-cyan transition-colors duration-200 font-medium"
                  >
                    Features
                  </a>
                  <a
                    href={getMarketingSiteUrl() + "/pricing"}
                    class="text-tokyo-night-fg-dark hover:text-tokyo-night-cyan transition-colors duration-200 font-medium"
                  >
                    Pricing
                  </a>
                  <a
                    href={getMarketingSiteUrl() + "/contact"}
                    class="text-tokyo-night-fg-dark hover:text-tokyo-night-cyan transition-colors duration-200 font-medium"
                  >
                    Contact
                  </a>
                </div>

                <!-- Auth Buttons - Desktop -->
                <div class="flex items-center space-x-3">
                  <a
                    href="{base}/login"
                    class={$page.url.pathname === `${base}/login/` || $page.url.pathname === `${base}/login`
                      ? "btn-primary"
                      : "btn-secondary"}
                  >
                    Sign In
                  </a>
                  <a
                    href="{base}/register"
                    class={$page.url.pathname === `${base}/register/` || $page.url.pathname === `${base}/register`
                      ? "btn-primary"
                      : "btn-secondary"}
                  >
                    Register
                  </a>
                </div>
              </div>

              <!-- Mobile Menu Button -->
              <div class="lg:hidden">
                <button
                  on:click={toggleMobileMenu}
                  class="p-2 rounded-lg text-tokyo-night-fg-dark hover:text-tokyo-night-fg hover:bg-tokyo-night-bg-light/50 transition-colors duration-200"
                  aria-label="Toggle mobile menu"
                >
                  <svg
                    class="w-6 h-6"
                    fill="none"
                    stroke="currentColor"
                    viewBox="0 0 24 24"
                  >
                    {#if mobileMenuOpen}
                      <path
                        stroke-linecap="round"
                        stroke-linejoin="round"
                        stroke-width="2"
                        d="M6 18L18 6M6 6l12 12"
                      />
                    {:else}
                      <path
                        stroke-linecap="round"
                        stroke-linejoin="round"
                        stroke-width="2"
                        d="M4 6h16M4 12h16M4 18h16"
                      />
                    {/if}
                  </svg>
                </button>
              </div>
            </div>

            <!-- Mobile Menu -->
            {#if mobileMenuOpen}
              <div
                class="lg:hidden border-t border-tokyo-night-fg-gutter bg-tokyo-night-bg/95 backdrop-blur-sm"
              >
                <div class="px-4 py-6 space-y-4">
                  <!-- Mobile Navigation Links -->
                  <div class="space-y-3">
                    <a
                      href={getMarketingSiteUrl()}
                      class="block text-tokyo-night-fg-dark hover:text-tokyo-night-cyan transition-colors duration-200 font-medium py-2"
                      on:click={() => (mobileMenuOpen = false)}
                    >
                      Features
                    </a>
                    <a
                      href={getMarketingSiteUrl() + "/#pricing"}
                      class="block text-tokyo-night-fg-dark hover:text-tokyo-night-cyan transition-colors duration-200 font-medium py-2"
                      on:click={() => (mobileMenuOpen = false)}
                    >
                      Pricing
                    </a>
                    <a
                      href={getMarketingSiteUrl() + "/contact"}
                      class="block text-tokyo-night-fg-dark hover:text-tokyo-night-cyan transition-colors duration-200 font-medium py-2"
                      on:click={() => (mobileMenuOpen = false)}
                    >
                      Contact
                    </a>
                  </div>

                  <!-- Mobile Auth Buttons -->
                  <div
                    class="pt-4 border-t border-tokyo-night-fg-gutter space-y-3"
                  >
                    <a
                      href="{base}/login"
                      class="{$page.url.pathname === `${base}/login/` || $page.url.pathname === `${base}/login`
                        ? 'btn-primary'
                        : 'btn-secondary'} w-full text-center"
                      on:click={() => (mobileMenuOpen = false)}
                    >
                      Sign In
                    </a>
                    <a
                      href="{base}/register"
                      class="{$page.url.pathname === `${base}/register/` || $page.url.pathname === `${base}/register`
                        ? 'btn-primary'
                        : 'btn-secondary'} w-full text-center"
                      on:click={() => (mobileMenuOpen = false)}
                    >
                      Register
                    </a>
                  </div>
                </div>
              </div>
            {/if}
          </div>
        </nav>

        <!-- Auth Page Content -->
        <main class="relative">
          <!-- Background Effects -->
          <div class="absolute inset-0 overflow-hidden pointer-events-none">
            <div
              class="absolute top-1/4 left-1/4 w-96 h-96 bg-tokyo-night-blue/5 rounded-full blur-3xl"
            />
            <div
              class="absolute bottom-1/4 right-1/4 w-96 h-96 bg-tokyo-night-cyan/5 rounded-full blur-3xl"
            />
          </div>

          <div class="relative max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
            <slot />
          </div>
        </main>
      </div>
    {/if}
  </div>
{/if}

<!-- Coming Soon Modal -->
<ComingSoonModal
  bind:show={showComingSoonModal}
  item={selectedFeature}
  on:close={closeComingSoonModal}
/>

<!-- Toast Notifications -->
<Toast />

<!-- Global Error Boundary -->
<ErrorBoundary />
