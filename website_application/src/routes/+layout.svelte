<script lang="ts">
  import { run } from 'svelte/legacy';

  import "../app.css";
  import { onMount } from "svelte";
  import { goto } from "$app/navigation";
  import { page } from "$app/stores";
  import { base, resolve } from "$app/paths";
  import { auth } from "$lib/stores/auth";
  import { sidebarStore } from "$lib/stores/sidebar.svelte";
import { getMarketingSiteUrl } from "$lib/config";
  import { getRouteInfo } from "$lib/navigation.js";
  import Sidebar from "$lib/components/Sidebar.svelte";
  import ComingSoonModal from "$lib/components/ComingSoonModal.svelte";
  import Toast from "$lib/components/Toast.svelte";
  import ErrorBoundary from "$lib/components/ErrorBoundary.svelte";
  import BetaBadge from "$lib/components/BetaBadge.svelte";
  import { Button } from "$lib/components/ui/button";
  import { TooltipProvider } from "$lib/components/ui/tooltip";
  interface User {
    id: string;
    email: string;
    name?: string;
    tenant_id?: string;
    email_verified?: boolean;
  }

  interface Props {
    children?: import('svelte').Snippet;
  }

  let { children }: Props = $props();

  let isAuthenticated = $state(false);
  let user = $state<User | null>(null);
  let loading = $state(true);
  let initialized = $state(false);
  let mobileMenuOpen = $state(false);

  // Coming Soon Modal state
  let showComingSoonModal = $state(false);
  /** @type {any} */
  let selectedFeature = $state(null);

  // Define public routes that don't require authentication
const loginPath = resolve("/login");
const loginIndexPath = resolve("/login/");
const registerPath = resolve("/register");
const registerIndexPath = resolve("/register/");
const verifyEmailPath = resolve("/verify-email");
const verifyEmailIndexPath = resolve("/verify-email/");
const forgotPasswordPath = resolve("/forgot-password");
const forgotPasswordIndexPath = resolve("/forgot-password/");
const resetPasswordPath = resolve("/reset-password");
const resetPasswordIndexPath = resolve("/reset-password/");
const dashboardPath = resolve("/");

const marketingBaseUrl = getMarketingSiteUrl();
const marketingAboutUrl = new URL("/about", marketingBaseUrl).toString();
const marketingPricingUrl = new URL("/pricing", marketingBaseUrl).toString();
const marketingContactUrl = new URL("/contact", marketingBaseUrl).toString();
const marketingRootUrl = new URL("/", marketingBaseUrl).toString();
const marketingPricingSectionUrl = new URL("/#pricing", marketingBaseUrl).toString();

function openExternal(url: string) {
  if (typeof window === "undefined") return;
  window.open(url, "_blank", "noreferrer");
}

  const publicRoutes = [loginPath, loginIndexPath, registerPath, registerIndexPath, verifyEmailPath, verifyEmailIndexPath, forgotPasswordPath, forgotPasswordIndexPath, resetPasswordPath, resetPasswordIndexPath];

  // Subscribe to auth store
  auth.subscribe((authState) => {
    isAuthenticated = authState.isAuthenticated;
    user = authState.user || null;
    loading = authState.loading;
    initialized = authState.initialized;
  });

  // Get current page title from navigation config
  let currentPageTitle = $derived.by(() => {
    const currentPath = $page.url.pathname.replace(base, '') || '/';
    const routeInfo = getRouteInfo(currentPath);
    return routeInfo ? routeInfo.name : 'Page';
  });

  // Reactive statement to handle route protection
  run(() => {
    // Only run route protection when not loading AND after initialization
    if (!loading && initialized) {
      const currentPath = $page.url.pathname;
      const isPublicRoute = publicRoutes.includes(currentPath);

      if (!isAuthenticated && !isPublicRoute) {
        // Redirect unauthenticated users to login
        goto(loginPath);
      } else if (isAuthenticated && isPublicRoute) {
        // Redirect authenticated users away from auth pages
        goto(dashboardPath);
      }
    }
  });

  onMount(async () => {
    await auth.checkAuth();
  });

  function logout() {
    auth.logout();
    goto(loginPath);
  }

  function handleComingSoon(event: CustomEvent) {
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
    sidebarStore.toggleCollapsed();
  }
</script>

{#if loading && !initialized}
  <!-- Loading Screen -->
  <div class="min-h-screen bg-background flex items-center justify-center">
    <div class="text-center">
      <div class="inline-flex items-center">
        <img
          src="/frameworks-dark-horizontal-lockup-transparent.svg"
          alt="FrameWorks"
          class="h-16 animate-pulse"
        />
      </div>
      <div class="mt-4 text-muted-foreground">Loading...</div>
    </div>
  </div>
{:else}
  <TooltipProvider>
  <div class="min-h-screen bg-background text-foreground">
    {#if isAuthenticated}
      <!-- Authenticated Layout with Sidebar -->
      <div class="flex flex-col h-screen">
        <!-- Top Navigation - Slab Style -->
        <nav
          class="bg-background border-b border-[hsl(var(--tn-fg-gutter)/0.3)] h-16 flex items-stretch justify-between sticky top-0 z-50"
        >
          <!-- Left Content (Padded) -->
          <div class="flex items-center px-6 gap-4 flex-1">
            <!-- Sidebar Toggle -->
            <button
              onclick={toggleSidebar}
              class="p-2 rounded-lg text-muted-foreground hover:text-foreground hover:bg-[hsl(var(--tn-bg-visual))] transition-colors cursor-pointer"
              title="Toggle Sidebar"
            >
              <svg class="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                {#if sidebarStore.collapsed}
                  <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 6h16M4 12h16M4 18h16" />
                {:else}
                  <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M11 19l-7-7 7-7m8 14l-7-7 7-7" />
                {/if}
              </svg>
            </button>
            
            <!-- FrameWorks Branding -->
            <a
              href={dashboardPath}
              class="flex items-center hover:opacity-80 transition-opacity"
            >
              <img
                src="/frameworks-dark-horizontal-lockup-transparent.svg"
                alt="FrameWorks"
                class="h-8"
              />
            </a>
            
            <!-- Beta Badge -->
            <BetaBadge />

            <!-- Page Title -->
            <div class="text-[hsl(var(--tn-fg-gutter))]">|</div>
            <h1 class="text-sm font-semibold text-foreground uppercase tracking-wide">
              {currentPageTitle}
            </h1>
          </div>

          <!-- Right Actions (Flush & Seamed) -->
          <div class="flex items-stretch">
            <!-- User Info (Seamed, Padded) -->
            <div class="hidden md:flex items-center px-6 border-l border-[hsl(var(--tn-fg-gutter)/0.3)] bg-[hsl(var(--tn-bg-dark)/0.3)]">
              <span class="text-sm text-muted-foreground"
                >Welcome, <span class="text-primary font-medium"
                  >{user?.name || user?.email}</span
                ></span
              >
            </div>

            <!-- Logout Button (Flush, Seamed, Full Height) -->
            <div class="w-24 border-l border-[hsl(var(--tn-fg-gutter)/0.3)]">
              <Button 
                variant="ghost" 
                onclick={logout}
                class="w-full h-full rounded-none hover:translate-y-0 hover:bg-[hsl(var(--tn-red)/0.1)] hover:text-[hsl(var(--tn-red))]"
              >
                Logout
              </Button>
            </div>
          </div>
        </nav>

        <!-- Main Content Area with Sidebar -->
        <div class="flex flex-1 overflow-hidden">
          <!-- Sidebar -->
          <Sidebar
            on:comingSoon={handleComingSoon}
            collapsed={sidebarStore.collapsed}
          />

          <!-- Page Content -->
          <main class="flex-1 overflow-hidden bg-background">
            {@render children?.()}
          </main>
        </div>
      </div>
    {:else}
      <!-- Unauthenticated Layout (Login/Register pages only) -->
      <div
        class="h-screen flex flex-col bg-gradient-to-br from-background via-card to-background"
      >
        <!-- Marketing-style Navigation for auth pages -->
        <nav
          class="sticky top-0 z-50 bg-[color-mix(in_srgb,hsl(var(--background))_98%,rgba(0,0,0,0.1))] backdrop-blur-[18px] border-b border-[hsl(var(--border)/0.45)] shadow-[0_12px_24px_rgba(6,15,65,0.15)]"
        >
          <div class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8">
            <div class="flex justify-between items-center h-16 lg:h-20">
              <!-- Logo Section -->
              <div class="flex items-center gap-3">
                <button
                  type="button"
                  class="flex items-center hover:opacity-80 transition-opacity"
                  onclick={() => openExternal(marketingRootUrl)}
                >
                  <img
                    src="/frameworks-dark-horizontal-lockup-transparent.svg"
                    alt="FrameWorks"
                    class="h-10"
                  />
                </button>
                <BetaBadge />
              </div>

              <!-- Desktop Navigation -->
              <div class="hidden lg:flex items-center space-x-8">
                <!-- Navigation Links -->
                <div class="flex items-center space-x-8">
                  <button
                    type="button"
                    class="group relative inline-flex items-center gap-1 text-foreground-dark hover:text-info transition-colors duration-200 font-medium after:content-[''] after:absolute after:left-0 after:right-0 after:bottom-[-4px] after:h-[2px] after:rounded-full after:bg-[linear-gradient(90deg,hsl(var(--primary)),hsl(var(--accent)))] after:scale-x-0 after:origin-left after:transition-transform after:duration-[250ms] after:ease-[ease] after:opacity-85 hover:after:scale-x-100"
                    onclick={() => openExternal(marketingAboutUrl)}
                  >
                    Features
                    <svg class="w-3 h-3 opacity-60 group-hover:opacity-100 group-hover:translate-x-0.5 group-hover:-translate-y-0.5 transition-all duration-200" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                      <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2.5" d="M7 17L17 7M17 7H7M17 7V17" />
                    </svg>
                  </button>
                  <button
                    type="button"
                    class="group relative inline-flex items-center gap-1 text-foreground-dark hover:text-info transition-colors duration-200 font-medium after:content-[''] after:absolute after:left-0 after:right-0 after:bottom-[-4px] after:h-[2px] after:rounded-full after:bg-[linear-gradient(90deg,hsl(var(--primary)),hsl(var(--accent)))] after:scale-x-0 after:origin-left after:transition-transform after:duration-[250ms] after:ease-[ease] after:opacity-85 hover:after:scale-x-100"
                    onclick={() => openExternal(marketingPricingUrl)}
                  >
                    Pricing
                    <svg class="w-3 h-3 opacity-60 group-hover:opacity-100 group-hover:translate-x-0.5 group-hover:-translate-y-0.5 transition-all duration-200" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                      <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2.5" d="M7 17L17 7M17 7H7M17 7V17" />
                    </svg>
                  </button>
                  <button
                    type="button"
                    class="group relative inline-flex items-center gap-1 text-foreground-dark hover:text-info transition-colors duration-200 font-medium after:content-[''] after:absolute after:left-0 after:right-0 after:bottom-[-4px] after:h-[2px] after:rounded-full after:bg-[linear-gradient(90deg,hsl(var(--primary)),hsl(var(--accent)))] after:scale-x-0 after:origin-left after:transition-transform after:duration-[250ms] after:ease-[ease] after:opacity-85 hover:after:scale-x-100"
                    onclick={() => openExternal(marketingContactUrl)}
                  >
                    Contact
                    <svg class="w-3 h-3 opacity-60 group-hover:opacity-100 group-hover:translate-x-0.5 group-hover:-translate-y-0.5 transition-all duration-200" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                      <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2.5" d="M7 17L17 7M17 7H7M17 7V17" />
                    </svg>
                  </button>
                </div>

                <!-- Auth Buttons - Desktop -->
                <div class="flex items-center space-x-8">
                  <a
                    href={loginPath}
                    class="relative text-foreground-dark hover:text-info transition-colors duration-200 font-medium after:content-[''] after:absolute after:left-0 after:right-0 after:bottom-[-4px] after:h-[2px] after:rounded-full after:bg-[linear-gradient(90deg,hsl(var(--primary)),hsl(var(--accent)))] after:origin-left after:transition-transform after:duration-[250ms] after:ease-[ease] after:opacity-85 {$page.url.pathname === loginIndexPath || $page.url.pathname === loginPath ? 'text-info after:scale-x-100' : 'after:scale-x-0 hover:after:scale-x-100'}"
                  >
                    Sign In
                  </a>
                  <a
                    href={registerPath}
                    class="relative text-foreground-dark hover:text-info transition-colors duration-200 font-medium after:content-[''] after:absolute after:left-0 after:right-0 after:bottom-[-4px] after:h-[2px] after:rounded-full after:bg-[linear-gradient(90deg,hsl(var(--primary)),hsl(var(--accent)))] after:origin-left after:transition-transform after:duration-[250ms] after:ease-[ease] after:opacity-85 {$page.url.pathname === registerIndexPath || $page.url.pathname === registerPath ? 'text-info after:scale-x-100' : 'after:scale-x-0 hover:after:scale-x-100'}"
                  >
                    Register
                  </a>
                </div>
              </div>

              <!-- Mobile Menu Button -->
              <div class="lg:hidden">
                <button
                  onclick={toggleMobileMenu}
                  class="p-2 rounded-lg text-muted-foreground hover:text-foreground hover:bg-background-light/50 transition-colors duration-200"
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
                class="lg:hidden border-t border-border bg-background/95 backdrop-blur-sm"
              >
                <div class="px-4 py-6 space-y-4">
                  <!-- Mobile Navigation Links -->
                  <div class="space-y-3">
                    <button
                      type="button"
                      class="block text-foreground-dark hover:text-info transition-colors duration-200 font-medium py-2 text-left w-full"
                      onclick={() => {
                        openExternal(marketingRootUrl);
                        mobileMenuOpen = false;
                      }}
                    >
                      Features
                    </button>
                    <button
                      type="button"
                      class="block text-foreground-dark hover:text-info transition-colors duration-200 font-medium py-2 text-left w-full"
                      onclick={() => {
                        openExternal(marketingPricingSectionUrl);
                        mobileMenuOpen = false;
                      }}
                    >
                      Pricing
                    </button>
                    <button
                      type="button"
                      class="block text-foreground-dark hover:text-info transition-colors duration-200 font-medium py-2 text-left w-full"
                      onclick={() => {
                        openExternal(marketingContactUrl);
                        mobileMenuOpen = false;
                      }}
                    >
                      Contact
                    </button>
                  </div>

                  <!-- Mobile Auth Buttons -->
                  <div
                    class="pt-4 border-t border-border space-y-3"
                  >
                    <Button
                      href={loginPath}
                      variant={$page.url.pathname === loginIndexPath || $page.url.pathname === loginPath ? "default" : "outline"}
                      class="w-full justify-center"
                      onclick={() => (mobileMenuOpen = false)}
                    >
                      Sign In
                    </Button>
                    <Button
                      href={registerPath}
                      variant={$page.url.pathname === registerIndexPath || $page.url.pathname === registerPath ? "default" : "outline"}
                      class="w-full justify-center"
                      onclick={() => (mobileMenuOpen = false)}
                    >
                      Register
                    </Button>
                  </div>
                </div>
              </div>
            {/if}
          </div>
        </nav>

        <!-- Auth Page Content -->
        <main class="relative flex-1 overflow-y-auto">
          <!-- Background Effects -->
          <div class="absolute inset-0 overflow-hidden pointer-events-none">
            <div
              class="absolute top-1/4 left-1/4 w-96 h-96 bg-primary/5 rounded-full blur-3xl"
></div>
            <div
              class="absolute bottom-1/4 right-1/4 w-96 h-96 bg-info/5 rounded-full blur-3xl"
></div>
          </div>

          {@render children?.()}
        </main>
      </div>
    {/if}
  </div>
  </TooltipProvider>
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
