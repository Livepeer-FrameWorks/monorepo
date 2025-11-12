<script>
  import { preventDefault } from 'svelte/legacy';
  import { onMount } from "svelte";
  import { goto } from "$app/navigation";
  import { resolve } from "$app/paths";
  import { auth } from "$lib/stores/auth";
  import { getMarketingSiteUrl } from "$lib/config";
  import { getIconComponent } from "$lib/iconUtils";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Turnstile } from "svelte-turnstile";

  let email = $state("");
  let password = $state("");
  let loading = $state(false);
  let error = $state("");

  // Bot protection
  const turnstileSiteKey = import.meta.env.VITE_TURNSTILE_AUTH_SITE_KEY || "";
  let turnstileToken = $state("");
  let turnstileWidgetId = $state("");

  const resetTurnstileWidget = () => {
    if (typeof window !== "undefined" && turnstileWidgetId) {
      try {
        window?.turnstile?.reset?.(turnstileWidgetId);
      } catch (err) {
        console.warn("Turnstile reset failed", err);
      }
    }
  };

  // Note: Authentication redirects are handled by +layout.svelte

  async function handleSubmit(event) {
    event.preventDefault();
    event.stopPropagation();

    if (!email || !password) {
      error = "Please fill in all fields";
      return;
    }

    if (turnstileSiteKey && !turnstileToken) {
      error = "Please complete the verification challenge.";
      return;
    }

    loading = true;
    error = "";

    try {
      const result = await auth.login(email, password, {
        turnstile_token: turnstileToken || undefined,
      });

      if (result.success) {
        goto(resolve("/"));
      } else {
        error = result.error || "Login failed";
      }
    } catch (err) {
      console.error('Login error:', err);
      error = /** @type {any} */ (err).message || "Login failed";
    } finally {
      loading = false;
      // Reset Turnstile token for next attempt
      if (turnstileSiteKey) {
        turnstileToken = "";
        resetTurnstileWidget();
      }
    }
  }

  const SvelteComponent = $derived(getIconComponent('Film'));
  const SvelteComponent_1 = $derived(getIconComponent('Bot'));
  const SvelteComponent_2 = $derived(getIconComponent('Globe'));
  const SvelteComponent_3 = $derived(getIconComponent('CreditCard'));
</script>

<svelte:head>
  <title>Login - FrameWorks</title>
</svelte:head>

<section class="min-h-screen bg-brand-surface-muted py-16 sm:py-24">
  <!-- Header -->
  <div class="text-center mb-12">
      <div class="flex justify-center mb-4">
        <img src="/frameworks-dark-logomark-transparent.svg" alt="FrameWorks" class="h-80 w-80 rounded-lg" />
      </div>
      <h1 class="text-4xl font-bold gradient-text mb-2">Welcome Back</h1>
      <p class="text-tokyo-night-fg-dark">Sign in to your FrameWorks account</p>
  </div>

  <!-- Two-column responsive grid -->
  <div class="grid grid-cols-1 lg:grid-cols-2 gap-8 items-start max-w-7xl mx-auto">
    <!-- Login Form -->
    <div class="marketing-slab auth-form-slab">
      <form onsubmit={preventDefault(handleSubmit)} class="space-y-6">
        <div>
          <label
            for="email"
            class="block text-sm font-medium mb-2 text-tokyo-night-fg-dark"
          >
            Email Address
          </label>
          <Input
            id="email"
            type="email"
            bind:value={email}
            placeholder="Enter your email"
            class="w-full"
          />
        </div>

        <div>
          <label
            for="password"
            class="block text-sm font-medium mb-2 text-tokyo-night-fg-dark"
          >
            Password
          </label>
          <Input
            id="password"
            type="password"
            bind:value={password}
            placeholder="Enter your password"
            class="w-full"
          />
        </div>

        {#if turnstileSiteKey}
          <div class="space-y-3">
            <p class="block text-sm font-medium text-tokyo-night-fg-dark">
              Verification *
            </p>
            <div
              class="inline-flex rounded-lg border border-border/50 bg-background/70 p-4"
            >
              <Turnstile
                siteKey={turnstileSiteKey}
                theme="dark"
                action="login_form"
                bind:widgetId={turnstileWidgetId}
                on:callback={({ detail }) => {
                  const value = detail?.token ?? detail ?? "";
                  turnstileToken = value;
                  error = "";
                }}
                on:error={() => {
                  turnstileToken = "";
                }}
                on:expire={() => {
                  turnstileToken = "";
                }}
              />
            </div>
          </div>
        {/if}

        <Button type="submit" class="w-full" disabled={loading}>
          {#if loading}
            <div class="loading-spinner mr-2"></div>
          {/if}
          Sign In
        </Button>
      </form>

      {#if error}
        <div
          class="mt-6 p-4 rounded-lg border bg-tokyo-night-red/10 border-tokyo-night-red/30"
        >
          <p class="text-tokyo-night-red">Error: {error}</p>
        </div>
      {/if}

      <div class="mt-6 text-center">
        <p class="text-tokyo-night-comment">
          Don't have an account?
          <a
            href={resolve("/register")}
            class="text-tokyo-night-blue hover:text-tokyo-night-cyan transition-colors"
          >
            Sign up
          </a>
        </p>
        </div>
      </div>

      <!-- Features Preview -->
      <div class="marketing-slab content-slab">
      <h3 class="text-lg font-semibold gradient-text mb-4">
        Why Choose FrameWorks?
      </h3>
      <div class="space-y-3 text-sm mb-6">
        <div class="flex items-center gap-3">
          <SvelteComponent class="w-5 h-5 text-tokyo-night-blue" />
          <span class="text-tokyo-night-fg-dark"
            >Multi-stream compositing with PiP and overlays</span
          >
        </div>
        <div class="flex items-center gap-3">
          <SvelteComponent_1 class="w-5 h-5 text-tokyo-night-green" />
          <span class="text-tokyo-night-fg-dark"
            >Real-time AI processing and analytics</span
          >
        </div>
        <div class="flex items-center gap-3">
          <SvelteComponent_2 class="w-5 h-5 text-tokyo-night-purple" />
          <span class="text-tokyo-night-fg-dark"
            >Hybrid cloud + self-hosted deployment</span
          >
        </div>
        <div class="flex items-center gap-3">
          <SvelteComponent_3 class="w-5 h-5 text-tokyo-night-yellow" />
          <span class="text-tokyo-night-fg-dark"
            >Generous free tier powered by Livepeer</span
          >
        </div>
      </div>
      <div class="text-center">
        <Button
          type="button"
          variant="outline"
          class="group"
          onclick={() => {
            if (typeof window !== "undefined") {
              window.open(getMarketingSiteUrl(), "_blank", "noreferrer");
            }
          }}
        >
          Learn More About FrameWorks
          <svg class="w-3 h-3 ml-1 opacity-60 group-hover:opacity-100 group-hover:translate-x-0.5 group-hover:-translate-y-0.5 transition-all duration-200" fill="none" stroke="currentColor" viewBox="0 0 24 24">
            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2.5" d="M7 17L17 7M17 7H7M17 7V17" />
          </svg>
        </Button>
      </div>
    </div>
  </div>
</section>
