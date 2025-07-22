<script>
  import { onMount } from "svelte";
  import { goto } from "$app/navigation";
  import { auth } from "$lib/stores/auth";
  import { getMarketingSiteUrl } from "$lib/config";

  let email = "";
  let password = "";
  let loading = false;
  let error = "";

  // Subscribe to auth store
  let isAuthenticated = false;
  auth.subscribe((authState) => {
    isAuthenticated = authState.isAuthenticated;
  });

  // Note: Authentication redirects are handled by +layout.svelte

  async function handleSubmit() {
    if (!email || !password) {
      error = "Please fill in all fields";
      return;
    }

    loading = true;
    error = "";

    try {
      const result = await auth.login(email, password);
      if (result.success) {
        goto("/");
      } else {
        error = result.error || "Login failed";
      }
    } catch (err) {
      error = /** @type {any} */ (err).message || "Login failed";
    } finally {
      loading = false;
    }
  }
</script>

<svelte:head>
  <title>Login - FrameWorks</title>
</svelte:head>

<div class="min-h-screen flex justify-center">
  <div class="max-w-md w-full space-y-8">
    <!-- Header -->
    <div class="text-center">
      <div class="flex justify-center mb-4">
        <img src="/logo.png" alt="FrameWorks" class="h-64 w-64 rounded-lg" />
      </div>
      <h1 class="text-4xl font-bold gradient-text mb-2">Welcome Back</h1>
      <p class="text-tokyo-night-fg-dark">Sign in to your FrameWorks account</p>
    </div>

    <!-- Login Form -->
    <div class="glow-card p-8">
      <form on:submit|preventDefault={handleSubmit} class="space-y-6">
        <div>
          <label
            for="email"
            class="block text-sm font-medium mb-2 text-tokyo-night-fg-dark"
          >
            Email Address
          </label>
          <input
            id="email"
            type="email"
            bind:value={email}
            placeholder="Enter your email"
            class="input"
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
          <input
            id="password"
            type="password"
            bind:value={password}
            placeholder="Enter your password"
            class="input"
            required
          />
        </div>

        <button type="submit" class="btn-primary w-full" disabled={loading}>
          {#if loading}
            <div class="loading-spinner mr-2" />
          {/if}
          Sign In
        </button>
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
            href="/register"
            class="text-tokyo-night-blue hover:text-tokyo-night-cyan transition-colors"
          >
            Sign up
          </a>
        </p>
      </div>
    </div>

    <!-- Features Preview -->
    <div class="glow-card p-6">
      <h3 class="text-lg font-semibold gradient-text mb-4">
        Why Choose FrameWorks?
      </h3>
      <div class="space-y-3 text-sm mb-6">
        <div class="flex items-center gap-3">
          <span class="text-tokyo-night-blue">🎬</span>
          <span class="text-tokyo-night-fg-dark"
            >Multi-stream compositing with PiP and overlays</span
          >
        </div>
        <div class="flex items-center gap-3">
          <span class="text-tokyo-night-green">🤖</span>
          <span class="text-tokyo-night-fg-dark"
            >Real-time AI processing and analytics</span
          >
        </div>
        <div class="flex items-center gap-3">
          <span class="text-tokyo-night-purple">🌐</span>
          <span class="text-tokyo-night-fg-dark"
            >Hybrid cloud + self-hosted deployment</span
          >
        </div>
        <div class="flex items-center gap-3">
          <span class="text-tokyo-night-yellow">💰</span>
          <span class="text-tokyo-night-fg-dark"
            >Generous free tier powered by Livepeer</span
          >
        </div>
      </div>
      <div class="text-center">
        <a href={getMarketingSiteUrl()} target="_blank" class="btn-secondary">
          Learn More About FrameWorks
        </a>
      </div>
    </div>
  </div>
</div>
