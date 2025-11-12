<script>
  import { preventDefault } from "svelte/legacy";

  import { onMount } from "svelte";
  import { goto } from "$app/navigation";
  import { resolve } from "$app/paths";
  import { auth } from "$lib/stores/auth";
  import { getIconComponent } from "$lib/iconUtils";
  import { RadioGroup, RadioGroupItem } from "$lib/components/ui/radio-group";
  import { Label } from "$lib/components/ui/label";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Turnstile } from "svelte-turnstile";

  const AlertTriangle = getIconComponent("AlertTriangle");

  let email = $state("");
  let password = $state("");
  let confirmPassword = $state("");
  /** @type {string | null} */
  let error = $state(null);
  let authLoading = $state(false);

  // Bot protection fields
  let phone_number = $state(""); // Honeypot - must remain empty (legacy only)
  const turnstileSiteKey = import.meta.env.VITE_TURNSTILE_AUTH_SITE_KEY || "";
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
        window?.turnstile?.reset?.(turnstileWidgetId);
      } catch (err) {
        console.warn("Turnstile reset failed", err);
      }
    }
  };

  // Subscribe to auth store
  auth.subscribe((authState) => {
    authLoading = authState.loading;
    error = authState.error;
  });

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

    // Note: Authentication redirects are handled by +layout.svelte
  });

  async function handleRegister() {
    // Registration enabled in all environments

    if (!email || !password || !confirmPassword) {
      error = "Please fill in all fields";
      return;
    }

    if (password !== confirmPassword) {
      error = "Passwords do not match";
      return;
    }

    if (password.length < 6) {
      error = "Password must be at least 6 characters long";
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

    const result = await auth.register(email, password, {
      phone_number, // Honeypot
      human_check,
      behavior: JSON.stringify(behaviorData),
      turnstile_token: turnstileToken || undefined,
    });

    if (result.success) {
      // Show verification message instead of redirecting
      error = null;
      goto(resolve("/verify-email"));
    }

    if (turnstileSiteKey) {
      turnstileToken = "";
      human_check = defaultHumanCheck;
      resetTurnstileWidget();
    }
  }

  /**
   * @param {KeyboardEvent} event
   */
  function handleKeypress(event) {
    if (event.key === "Enter") {
      handleRegister();
    }
  }
</script>

<svelte:head>
  <title>Register - FrameWorks</title>
</svelte:head>

<section class="min-h-screen bg-brand-surface-muted py-16 sm:py-24">
  <!-- Header -->
  <div class="text-center mb-12">
      <div class="flex justify-center mb-4">
        <img
          src="/frameworks-dark-logomark-transparent.svg"
          alt="FrameWorks"
          class="h-80 w-80 rounded-lg"
        />
      </div>
      <h1 class="text-4xl font-bold gradient-text mb-2">Join FrameWorks</h1>
      <p class="text-tokyo-night-fg-dark">
        Create your account to start streaming
      </p>

      <p class="text-center text-tokyo-night-comment mt-4">
        Already have an account?
        <a
          href={resolve("/login")}
          class="text-tokyo-night-cyan hover:text-tokyo-night-blue transition-colors duration-200 font-medium"
        >
          Sign in
        </a>
      </p>
  </div>

  <!-- Registration Content -->
  <div class="max-w-7xl mx-auto">
    <!-- Beta Disclaimer -->
    <div
      class="mb-8 p-4 rounded-lg border bg-tokyo-night-orange/10 border-tokyo-night-orange/30 max-w-4xl mx-auto"
    >
      <div class="flex items-start space-x-3">
        <div class="flex-shrink-0">
          <AlertTriangle class="w-5 h-5 text-tokyo-night-orange mt-0.5" />
        </div>
        <div>
          <h3 class="text-sm font-medium text-tokyo-night-orange">
            Beta Platform
          </h3>
          <p class="text-sm text-tokyo-night-fg-dark mt-1">
            FrameWorks is currently in beta and rapidly evolving. Features may
            change, and there could be occasional service interruptions as we
            improve the platform.
          </p>
        </div>
      </div>
    </div>

    <!-- Registration Form - Centered -->
    <div class="marketing-slab auth-form-slab max-w-2xl mx-auto">
      <form onsubmit={preventDefault(handleRegister)} class="space-y-6">
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
            required
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
            placeholder="Enter your password (min 6 characters)"
            class="w-full"
            onkeypress={handleKeypress}
            required
          />
        </div>

        <div>
          <label
            for="confirmPassword"
            class="block text-sm font-medium mb-2 text-tokyo-night-fg-dark"
          >
            Confirm Password
          </label>
          <Input
            id="confirmPassword"
            type="password"
            bind:value={confirmPassword}
            placeholder="Confirm your password"
            class="w-full"
            onkeypress={handleKeypress}
            required
          />
        </div>

        <!-- Honeypot field - hidden from users but visible to bots -->
        {#if !turnstileSiteKey}
          <div style="position: absolute; left: -9999px; visibility: hidden;">
            <label for="phone_number">Phone (leave blank)</label>
            <input
              id="phone_number"
              name="phone_number"
              type="text"
              bind:value={phone_number}
              tabindex="-1"
              autocomplete="off"
            />
          </div>
        {/if}

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
                action="register_form"
                bind:widgetId={turnstileWidgetId}
                on:callback={({ detail }) => {
                  const value = detail?.token ?? detail ?? "";
                  turnstileToken = value;
                  human_check = "human";
                  error = null;
                }}
                on:error={() => {
                  turnstileToken = "";
                  human_check = defaultHumanCheck;
                }}
                on:expire={() => {
                  turnstileToken = "";
                  human_check = defaultHumanCheck;
                }}
              />
            </div>
          </div>
        {/if}

        {#if !turnstileSiteKey}
          <!-- Human verification -->
          <div class="space-y-3">
            <p class="block text-sm font-medium text-tokyo-night-fg-dark">
              Please confirm you are human:
            </p>
            <RadioGroup bind:value={human_check} required class="space-y-2">
              <div class="flex items-center space-x-2">
                <RadioGroupItem value="human" id="human-check-human" />
                <Label
                  for="human-check-human"
                  class="text-sm text-tokyo-night-fg-dark"
                >
                  I am a human
                </Label>
              </div>
              <div class="flex items-center space-x-2">
                <RadioGroupItem value="robot" id="human-check-robot" />
                <Label
                  for="human-check-robot"
                  class="text-sm text-tokyo-night-fg-dark"
                >
                  I am a robot
                </Label>
              </div>
            </RadioGroup>
          </div>
        {/if}

        <Button
          type="submit"
          class="w-full"
          disabled={authLoading || (turnstileSiteKey && !turnstileToken)}
        >
          {#if authLoading}
            <div class="loading-spinner mr-2"></div>
          {/if}
          Create Account
        </Button>
      </form>

      {#if error}
        <div
          class="mt-6 p-4 rounded-lg border bg-tokyo-night-red/10 border-tokyo-night-red/30"
        >
          <p class="text-tokyo-night-red">Error: {error}</p>
        </div>
      {/if}
    </div>
  </div>
</section>
