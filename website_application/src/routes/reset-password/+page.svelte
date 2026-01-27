<script lang="ts">
  import { onMount } from "svelte";
  import { page } from "$app/state";
  import { resolve } from "$app/paths";
  import { auth } from "$lib/stores/auth";
  import { getIconComponent } from "$lib/iconUtils";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";

  let token = $state("");
  let password = $state("");
  let confirmPassword = $state("");
  let loading = $state(false);
  let success = $state(false);
  let error = $state("");

  // Icons
  const KeyIcon = getIconComponent("Key");
  const CheckCircleIcon = getIconComponent("CheckCircle");
  const AlertTriangleIcon = getIconComponent("AlertTriangle");

  onMount(() => {
    token = page.url.searchParams.get("token") || "";
    if (!token) {
      error = "No reset token provided. Please request a new password reset link.";
    }
  });

  async function handleSubmit(event: Event) {
    event.preventDefault();

    if (!password || !confirmPassword) {
      error = "Please fill in all fields";
      return;
    }

    if (password.length < 8) {
      error = "Password must be at least 8 characters";
      return;
    }

    if (password !== confirmPassword) {
      error = "Passwords do not match";
      return;
    }

    loading = true;
    error = "";

    try {
      const result = await auth.resetPassword(token, password);

      if (result.success) {
        success = true;
      } else {
        error = result.error || "Failed to reset password";
      }
    } catch {
      error = "Failed to reset password. The link may be invalid or expired.";
    } finally {
      loading = false;
    }
  }
</script>

<svelte:head>
  <title>Reset Password - FrameWorks</title>
</svelte:head>

<section class="min-h-full bg-brand-surface-muted flex items-center justify-center p-4 sm:p-8">
  <div class="w-full max-w-md">
    {#if success}
      <!-- Success State Slab -->
      <div class="slab">
        <div class="slab-header">
          <div class="flex items-center gap-2">
            <CheckCircleIcon class="w-4 h-4 text-success" />
            <h3>Password Reset</h3>
          </div>
        </div>
        <div class="slab-body--padded">
          <p class="text-muted-foreground">
            Your password has been reset successfully. You can now sign in with your new password.
          </p>
        </div>
        <div class="slab-actions">
          <Button href={resolve("/login")} class="w-full justify-center">
            Continue to Sign In
          </Button>
        </div>
      </div>
    {:else if !token}
      <!-- Missing Token Error State Slab -->
      <div class="slab">
        <div class="slab-header">
          <div class="flex items-center gap-2">
            <AlertTriangleIcon class="w-4 h-4 text-destructive" />
            <h3>Invalid Link</h3>
          </div>
        </div>
        <div class="slab-body--padded">
          <p class="text-destructive">{error}</p>
        </div>
        <div class="slab-actions">
          <Button href={resolve("/forgot-password")} class="w-full justify-center">
            Request New Reset Link
          </Button>
        </div>
      </div>
    {:else}
      <!-- Form State Slab -->
      <div class="slab">
        <div class="slab-header">
          <div class="flex items-center gap-2">
            <KeyIcon class="w-4 h-4 text-primary" />
            <h3>Set New Password</h3>
          </div>
        </div>
        <div class="slab-body--padded">
          <p class="text-muted-foreground mb-4">Enter your new password below.</p>

          <form onsubmit={handleSubmit} class="space-y-4">
            <div>
              <label for="password" class="block text-sm font-medium mb-2 text-muted-foreground">
                New Password
              </label>
              <Input
                id="password"
                type="password"
                bind:value={password}
                placeholder="Enter new password"
                disabled={loading}
              />
              <p class="mt-1 text-xs text-muted-foreground">Must be at least 8 characters</p>
            </div>

            <div>
              <label
                for="confirmPassword"
                class="block text-sm font-medium mb-2 text-muted-foreground"
              >
                Confirm Password
              </label>
              <Input
                id="confirmPassword"
                type="password"
                bind:value={confirmPassword}
                placeholder="Confirm new password"
                disabled={loading}
              />
            </div>

            {#if error}
              <div class="p-3 border bg-destructive/10 border-destructive/30">
                <p class="text-destructive text-sm">{error}</p>
              </div>
            {/if}

            <Button
              type="submit"
              class="w-full"
              disabled={loading || !password || !confirmPassword}
            >
              {#if loading}
                <div class="loading-spinner mr-2"></div>
              {/if}
              Reset Password
            </Button>
          </form>
        </div>
        <div class="slab-actions">
          <Button href={resolve("/login")} variant="ghost" class="w-full justify-center">
            Back to Sign In
          </Button>
        </div>
      </div>
    {/if}
  </div>
</section>
