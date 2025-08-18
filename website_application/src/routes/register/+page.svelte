<script>
  import { onMount } from "svelte";
  import { goto } from "$app/navigation";
  import { base } from "$app/paths";
  import { auth } from "$lib/stores/auth";

  let email = "";
  let password = "";
  let confirmPassword = "";
  let loading = false;
  /** @type {string | null} */
  let error = null;
  let isAuthenticated = false;
  let authLoading = false;
  let authInitialized = false;

  // Bot protection fields
  let phone_number = ""; // Honeypot - must remain empty
  let human_check = ""; // "human" or "robot" - expect "human"
  let formShownAt = 0;
  let userInteractions = {
    mouse: false,
    typed: false,
    focused: false
  };

  // Check if we're in development mode
  const isDev = import.meta.env.DEV;

  // Subscribe to auth store
  auth.subscribe((authState) => {
    isAuthenticated = authState.isAuthenticated;
    loading = authState.loading;
    authLoading = authState.loading;
    authInitialized = authState.initialized;
    error = authState.error;
  });

  onMount(() => {
    // Record when form was shown
    formShownAt = Date.now();

    // Add interaction listeners
    const form = document.querySelector('form');
    if (form) {
      // Mouse movement detection
      form.addEventListener('mousemove', () => {
        userInteractions.mouse = true;
      });

      // Typing detection
      form.addEventListener('input', () => {
        userInteractions.typed = true;
      });

      // Focus detection
      form.addEventListener('focusin', () => {
        userInteractions.focused = true;
      });
    }

    // Note: Authentication redirects are handled by +layout.svelte
  });

  async function handleRegister() {
    // If registration is disabled, redirect to contact page
    if (!isDev) {
      window.open("https://frameworks.network/contact", "_blank");
      return;
    }

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
      focused: userInteractions.focused
    };

    const result = await auth.register(email, password, {
      phone_number, // Honeypot
      human_check,
      behavior: JSON.stringify(behaviorData)
    });
    
    if (result.success) {
      // Show verification message instead of redirecting
      error = null;
      goto(`${base}/verify-email`);
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

<div class="min-h-screen flex justify-center">
  <div class="max-w-md w-full space-y-8">
    <!-- Header -->
    <div class="text-center">
      <div class="flex justify-center mb-4">
        <img src="/logo.png" alt="FrameWorks" class="h-64 w-64 rounded-lg" />
      </div>
      <h1 class="text-4xl font-bold gradient-text mb-2">Join FrameWorks</h1>
      <p class="text-tokyo-night-fg-dark">
        Create your account to start streaming
      </p>

      <p class="text-center text-tokyo-night-comment">
        Already have an account?
        <a 
          href="{base}/login" 
          class="text-tokyo-night-cyan hover:text-tokyo-night-blue transition-colors duration-200 font-medium"
        >
          Sign in
        </a>
      </p>
    </div>

    <!-- Private Beta Notice -->
    {#if !isDev}
      <div
        class="glow-card p-6 bg-tokyo-night-blue/10 border-tokyo-night-blue/30"
      >
        <div class="flex items-center space-x-2 mb-3">
          <svg
            class="w-5 h-5 text-tokyo-night-blue"
            fill="none"
            stroke="currentColor"
            viewBox="0 0 24 24"
          >
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              stroke-width="2"
              d="M12 15v2m-6 4h12a2 2 0 002-2v-6a2 2 0 00-2-2H6a2 2 0 00-2 2v6a2 2 0 002 2zm10-10V7a4 4 0 00-8 0v4h8z"
            />
          </svg>
          <h3 class="font-semibold text-tokyo-night-blue">
            Early Access Program
          </h3>
        </div>
        <div class="space-y-3 text-sm text-tokyo-night-fg-dark">
          <p>
            FrameWorks is currently in private beta. Contact us for early access
            to get started before everyone else.
          </p>
          <p>
            The dashboard will be open for public registrations soon, and we're
            continuously merging in new features leading up to our full launch
            at <strong class="text-tokyo-night-cyan"
              >IBC Amsterdam in September</strong
            >.
          </p>
        </div>
        <div class="mt-4">
          <a
            href="https://frameworks.network/contact"
            target="_blank"
            class="inline-flex items-center text-sm font-medium text-tokyo-night-blue hover:text-tokyo-night-cyan transition-colors"
          >
            Contact us for early access
            <svg
              class="w-4 h-4 ml-1"
              fill="none"
              stroke="currentColor"
              viewBox="0 0 24 24"
            >
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                stroke-width="2"
                d="M10 6H6a2 2 0 00-2 2v10a2 2 0 002 2h10a2 2 0 002-2v-4M14 4h6m0 0v6m0-6L10 14"
              />
            </svg>
          </a>
        </div>
      </div>
    {/if}

    <!-- Registration Form -->
    <div class="glow-card p-8 {!isDev ? 'opacity-60' : ''}">
      <form on:submit|preventDefault={handleRegister} class="space-y-6">
        <div>
          <label
            for="email"
            class="block text-sm font-medium mb-2 text-tokyo-night-fg-dark {!isDev
              ? 'opacity-60'
              : ''}"
          >
            Email Address
          </label>
          <input
            id="email"
            type="email"
            bind:value={email}
            placeholder="Enter your email"
            class="input {!isDev ? 'cursor-not-allowed opacity-50' : ''}"
            disabled={!isDev}
            required
          />
        </div>

        <div>
          <label
            for="password"
            class="block text-sm font-medium mb-2 text-tokyo-night-fg-dark {!isDev
              ? 'opacity-60'
              : ''}"
          >
            Password
          </label>
          <input
            id="password"
            type="password"
            bind:value={password}
            placeholder="Enter your password (min 6 characters)"
            class="input {!isDev ? 'cursor-not-allowed opacity-50' : ''}"
            disabled={!isDev}
            on:keypress={handleKeypress}
            required
          />
        </div>

        <div>
          <label
            for="confirmPassword"
            class="block text-sm font-medium mb-2 text-tokyo-night-fg-dark {!isDev
              ? 'opacity-60'
              : ''}"
          >
            Confirm Password
          </label>
          <input
            id="confirmPassword"
            type="password"
            bind:value={confirmPassword}
            placeholder="Confirm your password"
            class="input {!isDev ? 'cursor-not-allowed opacity-50' : ''}"
            disabled={!isDev}
            on:keypress={handleKeypress}
            required
          />
        </div>

        <!-- Honeypot field - hidden from users but visible to bots -->
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

        <!-- Human verification -->
        <div class="space-y-3">
          <fieldset class="space-y-2">
            <legend class="block text-sm font-medium mb-2 text-tokyo-night-fg-dark {!isDev ? 'opacity-60' : ''}">
              Please confirm you are human:
            </legend>
            <div class="flex items-center space-x-2">
              <input
                id="human-check-human"
                type="radio"
                name="human_check"
                value="human"
                bind:group={human_check}
                class="radio {!isDev ? 'cursor-not-allowed opacity-50' : ''}"
                disabled={!isDev}
                required
              />
              <label for="human-check-human" class="text-sm text-tokyo-night-fg-dark {!isDev ? 'opacity-60' : ''}">
                I am a human
              </label>
            </div>
            <div class="flex items-center space-x-2">
              <input
                id="human-check-robot"
                type="radio"
                name="human_check"
                value="robot"
                bind:group={human_check}
                class="radio {!isDev ? 'cursor-not-allowed opacity-50' : ''}"
                disabled={!isDev}
              />
              <label for="human-check-robot" class="text-sm text-tokyo-night-fg-dark {!isDev ? 'opacity-60' : ''}">
                I am a robot
              </label>
            </div>
          </fieldset>
        </div>

        <button type="submit" class="btn-primary w-full" disabled={authLoading}>
          {#if authLoading}
            <div class="loading-spinner mr-2" />
          {/if}
          {isDev ? "Create Account" : "Contact us for early access"}
        </button>
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
</div>
