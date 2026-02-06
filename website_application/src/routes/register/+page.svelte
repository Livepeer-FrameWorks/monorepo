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

  let attribution = $state({
    utm_source: "",
    utm_medium: "",
    utm_campaign: "",
    utm_content: "",
    utm_term: "",
    referral_code: "",
    landing_page: "",
  });

  const readAttribution = () => {
    if (typeof window === "undefined") {
      return;
    }

    const params = new URLSearchParams(window.location.search);
    const referralCode = params.get("referral_code") || params.get("ref") || "";
    const fromParams = {
      utm_source: params.get("utm_source") || "",
      utm_medium: params.get("utm_medium") || "",
      utm_campaign: params.get("utm_campaign") || "",
      utm_content: params.get("utm_content") || "",
      utm_term: params.get("utm_term") || "",
      referral_code: referralCode,
      landing_page: window.location.href,
    };

    let stored = {};
    try {
      stored = JSON.parse(window.sessionStorage.getItem("signup_attribution") || "{}");
    } catch {
      stored = {};
    }

    attribution = {
      ...attribution,
      ...stored,
      ...Object.fromEntries(Object.entries(fromParams).filter(([, value]) => value)),
    };

    window.sessionStorage.setItem("signup_attribution", JSON.stringify(attribution));
  };

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

    readAttribution();

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

    if (password.length < 8) {
      error = "Password must be at least 8 characters long";
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
      turnstileToken: turnstileToken || undefined,
      ...attribution,
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

<section class="min-h-full bg-brand-surface-muted flex items-center justify-center p-4 sm:p-8">
  <div class="grid grid-cols-1 lg:grid-cols-2 gap-8 lg:gap-12 items-center w-full max-w-5xl">
    <!-- Left: Branding -->
    <div class="flex flex-col items-center lg:items-start text-center lg:text-left">
      <!-- Logo + Title inline -->
      <div class="flex items-center gap-4 mb-6">
        <img src="/frameworks-dark-logomark-transparent.svg" alt="FrameWorks" class="h-16 w-16" />
        <div class="text-left">
          <h1 class="text-3xl sm:text-4xl font-bold gradient-text">Join FrameWorks</h1>
          <p class="text-muted-foreground">Create your account to start streaming</p>
        </div>
      </div>

      <!-- Beta Disclaimer -->
      <div class="p-4 border bg-warning-alt/10 border-warning-alt/30 mb-6 w-full">
        <div class="flex items-start space-x-3">
          <div class="flex-shrink-0">
            <AlertTriangle class="w-5 h-5 text-warning-alt mt-0.5" />
          </div>
          <div>
            <h3 class="text-sm font-medium text-warning-alt">Alpha Release</h3>
            <p class="text-sm text-muted-foreground mt-1">
              FrameWorks is currently in alpha and rapidly evolving. Features may change, and there
              could be occasional service interruptions.
            </p>
          </div>
        </div>
      </div>

      <p class="text-muted-foreground">
        Already have an account? <a
          href={resolve("/login")}
          class="text-info font-medium underline underline-offset-4 hover:text-primary transition-colors"
        >
          Sign in
        </a>
      </p>
    </div>

    <!-- Right: Registration Form -->
    <div class="slab">
      <div class="slab-header">
        <h3>Create Account</h3>
      </div>

      <form id="register-form" onsubmit={preventDefault(handleRegister)}>
        <!-- Email field -->
        <div class="px-4 py-3 border-b border-[hsl(var(--tn-fg-gutter)/0.3)]">
          <label
            for="email"
            class="block text-xs font-medium text-muted-foreground uppercase tracking-wider mb-2"
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

        <!-- Password field -->
        <div class="px-4 py-3 border-b border-[hsl(var(--tn-fg-gutter)/0.3)]">
          <label
            for="password"
            class="block text-xs font-medium text-muted-foreground uppercase tracking-wider mb-2"
          >
            Password
          </label>
          <Input
            id="password"
            type="password"
            bind:value={password}
            placeholder="Min 8 characters"
            class="w-full"
            onkeypress={handleKeypress}
            required
          />
        </div>

        <!-- Confirm Password field -->
        <div class="px-4 py-3 border-b border-[hsl(var(--tn-fg-gutter)/0.3)]">
          <label
            for="confirmPassword"
            class="block text-xs font-medium text-muted-foreground uppercase tracking-wider mb-2"
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
          <div class="sr-only">
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
          <div class="px-4 py-3 border-b border-[hsl(var(--tn-fg-gutter)/0.3)]">
            <p
              class="block text-xs font-medium text-muted-foreground uppercase tracking-wider mb-2"
            >
              Verification
            </p>
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
        {/if}

        {#if !turnstileSiteKey}
          <!-- Human verification -->
          <div class="px-4 py-3 border-b border-[hsl(var(--tn-fg-gutter)/0.3)]">
            <p
              class="block text-xs font-medium text-muted-foreground uppercase tracking-wider mb-2"
            >
              Verification
            </p>
            <RadioGroup bind:value={human_check} required class="flex gap-6">
              <div class="flex items-center gap-2">
                <RadioGroupItem value="human" id="human-check-human" />
                <Label for="human-check-human" class="cursor-pointer">I am human</Label>
              </div>
              <div class="flex items-center gap-2">
                <RadioGroupItem value="robot" id="human-check-robot" />
                <Label for="human-check-robot" class="cursor-pointer">I am a robot</Label>
              </div>
            </RadioGroup>
          </div>
        {/if}

        {#if error}
          <div class="px-4 py-3 bg-destructive/10 border-b border-destructive/30">
            <p class="block text-xs font-medium text-destructive uppercase tracking-wider mb-2">
              Error
            </p>
            <p class="text-destructive text-sm">{error}</p>
          </div>
        {/if}
      </form>

      <div class="slab-actions">
        <Button
          type="submit"
          form="register-form"
          variant="ghost"
          disabled={authLoading || (turnstileSiteKey && !turnstileToken)}
        >
          {#if authLoading}
            <div class="loading-spinner mr-2"></div>
          {/if}
          Create Account
        </Button>
      </div>
      <div class="slab-actions slab-actions--row">
        <Button href={resolve("/login")} variant="ghost">Already have an account?</Button>
      </div>
    </div>
  </div>
</section>
