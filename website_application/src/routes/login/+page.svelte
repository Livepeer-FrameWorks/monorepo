<script lang="ts">
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
  import { RadioGroup, RadioGroupItem } from "$lib/components/ui/radio-group";
  import { Label } from "$lib/components/ui/label";
  import WalletConnect from "$lib/components/WalletConnect.svelte";

  let email = $state("");
  let password = $state("");
  let loading = $state(false);
  let error = $state("");

  // Bot protection fields
  let phone_number = $state(""); // Honeypot - must remain empty
  const turnstileSiteKey = (import.meta as any).env.VITE_TURNSTILE_AUTH_SITE_KEY || "";
  const defaultHumanCheck = turnstileSiteKey ? "robot" : "human";

  let human_check = $state(defaultHumanCheck); // "human" or "robot" - expect "human"
  let formShownAt = 0;
  let userInteractions = {
    mouse: false,
    typed: false,
    focused: false,
  };
  let turnstileToken = $state("");
  let turnstileWidgetId = $state("");

  const resetTurnstileWidget = () => {
    if (typeof window !== "undefined" && turnstileWidgetId) {
      try {
        // @ts-ignore - turnstile global
        window?.turnstile?.reset?.(turnstileWidgetId);
      } catch (err) {
        console.warn("Turnstile reset failed", err);
      }
    }
  };

  // Note: Authentication redirects are handled by +layout.svelte

  onMount(() => {
    // Record when form was shown
    formShownAt = Date.now();

    // Add interaction listeners
    const form = document.querySelector("form");
    if (form) {
      // Mouse movement detection
      form.addEventListener("mousemove", () => {
        userInteractions.mouse = true;
      });

      // Typing detection
      form.addEventListener("input", () => {
        userInteractions.typed = true;
      });

      // Focus detection
      form.addEventListener("focusin", () => {
        userInteractions.focused = true;
      });
    }
  });

  async function handleSubmit(event: Event) {
    // @ts-ignore
    event.preventDefault();
    event.stopPropagation();

    if (!email || !password) {
      error = "Please fill in all fields";
      return;
    }

    // Prepare bot protection data
    const behaviorData = {
      formShownAt,
      submittedAt: Date.now(),
      mouse: userInteractions.mouse,
      typed: userInteractions.typed,
      focused: userInteractions.focused,
    };

    if (turnstileSiteKey && !turnstileToken) {
      error = "Please complete the verification challenge.";
      return;
    }

    loading = true;
    error = "";

    try {
      const result = await auth.login(email, password, {
        turnstileToken: turnstileToken || undefined,
        phone_number, // Honeypot
        human_check,
        behavior: JSON.stringify(behaviorData),
      });

      if (result.success) {
        goto(resolve("/"));
      } else {
        error = result.error || "Login failed";
      }
    } catch (err: any) {
      console.error('Login error:', err);
      error = err.message || "Login failed";
    } finally {
      loading = false;
      // Reset bot protection for next attempt
      if (turnstileSiteKey) {
        turnstileToken = "";
        human_check = defaultHumanCheck;
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

<section class="min-h-full bg-brand-surface-muted flex items-center justify-center p-4 sm:p-8">
  <div class="grid grid-cols-1 lg:grid-cols-2 gap-8 lg:gap-12 items-center w-full max-w-5xl">
    <!-- Left: Branding -->
    <div class="flex flex-col items-center lg:items-start text-center lg:text-left">
      <!-- Logo + Title inline -->
      <div class="flex items-center gap-4 mb-8">
        <img src="/frameworks-dark-logomark-transparent.svg" alt="FrameWorks" class="h-16 w-16" />
        <div class="text-left">
          <h1 class="text-3xl sm:text-4xl font-bold gradient-text">Welcome Back</h1>
          <p class="text-muted-foreground">Sign in to your FrameWorks account</p>
        </div>
      </div>

      <!-- Features -->
      <div class="space-y-3 text-sm">
        <div class="flex items-center gap-3">
          <SvelteComponent class="w-5 h-5 text-tokyo-night-blue" />
          <span class="text-muted-foreground">Multi-stream compositing with PiP and overlays</span>
        </div>
        <div class="flex items-center gap-3">
          <SvelteComponent_1 class="w-5 h-5 text-tokyo-night-cyan" />
          <span class="text-muted-foreground">Real-time AI processing and analytics</span>
        </div>
        <div class="flex items-center gap-3">
          <SvelteComponent_2 class="w-5 h-5 text-tokyo-night-purple" />
          <span class="text-muted-foreground">Hybrid cloud + self-hosted deployment</span>
        </div>
        <div class="flex items-center gap-3">
          <SvelteComponent_3 class="w-5 h-5 text-tokyo-night-teal" />
          <span class="text-muted-foreground">Generous free tier powered by Livepeer</span>
        </div>
      </div>

      <div class="mt-8">
        <Button
          type="button"
          variant="ghost"
          class="group text-muted-foreground hover:text-foreground"
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

    <!-- Right: Login Form -->
    <div class="slab">
      <div class="slab-header">
        <h3>Sign In</h3>
      </div>

      <form id="login-form" onsubmit={preventDefault(handleSubmit)}>
        <!-- Email field -->
        <div class="px-4 py-3 border-b border-[hsl(var(--tn-fg-gutter)/0.3)]">
          <label for="email" class="block text-xs font-medium text-muted-foreground uppercase tracking-wider mb-2">
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

        <!-- Password field -->
        <div class="px-4 py-3 border-b border-[hsl(var(--tn-fg-gutter)/0.3)]">
          <label for="password" class="block text-xs font-medium text-muted-foreground uppercase tracking-wider mb-2">
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

        <!-- Honeypot field (hidden, should remain empty) -->
        {#if !turnstileSiteKey}
          <div class="hidden" aria-hidden="true">
            <Input
              type="text"
              bind:value={phone_number}
              tabindex={-1}
              autocomplete="off"
            />
          </div>
        {/if}

        <!-- Human verification (fallback when Turnstile not configured) -->
        {#if !turnstileSiteKey}
          <div class="px-4 py-3 border-b border-[hsl(var(--tn-fg-gutter)/0.3)]">
            <p class="block text-xs font-medium text-muted-foreground uppercase tracking-wider mb-2">
              Verification
            </p>
            <RadioGroup bind:value={human_check} class="flex gap-6">
              <div class="flex items-center gap-2">
                <RadioGroupItem value="human" id="human" />
                <Label for="human" class="cursor-pointer">I am human</Label>
              </div>
              <div class="flex items-center gap-2">
                <RadioGroupItem value="robot" id="robot" />
                <Label for="robot" class="cursor-pointer">I am a robot</Label>
              </div>
            </RadioGroup>
          </div>
        {/if}

        {#if turnstileSiteKey}
          <div class="px-4 py-3 border-b border-[hsl(var(--tn-fg-gutter)/0.3)]">
            <p class="block text-xs font-medium text-muted-foreground uppercase tracking-wider mb-2">
              Verification
            </p>
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
        {/if}

        {#if error}
          <div class="px-4 py-3 bg-destructive/10 border-b border-destructive/30">
            <p class="block text-xs font-medium text-destructive uppercase tracking-wider mb-2">
              Error
            </p>
            <p class="text-destructive text-sm">{error}</p>
            {#if error.toLowerCase().includes("not verified") || error.toLowerCase().includes("verify")}
              <p class="mt-2 text-sm text-muted-foreground">
                Need a new verification link?{" "}
                <a
                  href={resolve("/verify-email")}
                  class="text-info underline underline-offset-2 hover:text-primary"
                >
                  Resend verification email
                </a>
              </p>
            {/if}
          </div>
        {/if}
      </form>

      <div class="slab-actions">
        <Button
          type="submit"
          form="login-form"
          variant="ghost"
          disabled={loading || (!turnstileSiteKey && human_check !== "human")}
        >
          {#if loading}
            <div class="loading-spinner mr-2"></div>
          {/if}
          Sign In
        </Button>
      </div>
      <div class="slab-actions slab-actions--row">
        <Button href={resolve("/forgot-password")} variant="ghost">
          Forgot password?
        </Button>
        <Button href={resolve("/register")} variant="ghost">
          Create account
        </Button>
      </div>

      <!-- Wallet Login Option -->
      <WalletConnect mode="login" />
    </div>
  </div>
</section>