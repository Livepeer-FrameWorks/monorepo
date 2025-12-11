<script lang="ts">
  import { resolve } from "$app/paths";
  import { auth } from "$lib/stores/auth";
  import { getIconComponent } from "$lib/iconUtils";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";

  let email = $state("");
  let loading = $state(false);
  let submitted = $state(false);
  let error = $state("");

  // Icons
  const KeyIcon = getIconComponent("Key");
  const CheckCircleIcon = getIconComponent("CheckCircle");

  async function handleSubmit(event: Event) {
    event.preventDefault();

    if (!email.trim()) {
      error = "Please enter your email address";
      return;
    }

    loading = true;
    error = "";

    try {
      const result = await auth.forgotPassword(email.trim());

      if (result.success) {
        submitted = true;
      } else {
        // Backend may still return success to not reveal if email exists
        // but we handle any explicit errors
        error = result.error || "Failed to send reset email";
      }
    } catch {
      error = "Network error. Please try again.";
    } finally {
      loading = false;
    }
  }
</script>

<svelte:head>
  <title>Forgot Password - FrameWorks</title>
</svelte:head>

<section class="min-h-full bg-brand-surface-muted flex items-center justify-center p-4 sm:p-8">
  <div class="w-full max-w-md">
    {#if submitted}
      <!-- Success State Slab -->
      <div class="slab">
        <div class="slab-header">
          <div class="flex items-center gap-2">
            <CheckCircleIcon class="w-4 h-4 text-success" />
            <h3>Check Your Email</h3>
          </div>
        </div>
        <div class="slab-body--padded">
          <p class="text-muted-foreground mb-4">
            If an account exists for <span class="text-foreground font-medium">{email}</span>,
            you will receive a password reset link shortly.
          </p>
          <div class="space-y-2 text-sm text-muted-foreground">
            <p>• Check your spam/junk folder if you don't see the email</p>
            <p>• The reset link expires in 1 hour</p>
          </div>
        </div>
        <div class="slab-actions">
          <Button href={resolve("/login")} class="w-full justify-center">
            Return to Sign In
          </Button>
        </div>
      </div>
    {:else}
      <!-- Form State Slab -->
      <div class="slab">
        <div class="slab-header">
          <div class="flex items-center gap-2">
            <KeyIcon class="w-4 h-4 text-primary" />
            <h3>Reset Password</h3>
          </div>
        </div>
        <div class="slab-body--padded">
          <p class="text-muted-foreground mb-4">
            Enter your email address and we'll send you a link to reset your password.
          </p>

          <form onsubmit={handleSubmit} class="space-y-4">
            <div>
              <label for="email" class="block text-sm font-medium mb-2 text-muted-foreground">
                Email Address
              </label>
              <Input
                id="email"
                type="email"
                bind:value={email}
                placeholder="Enter your email"
                disabled={loading}
              />
            </div>

            {#if error}
              <div class="p-3 border bg-destructive/10 border-destructive/30">
                <p class="text-destructive text-sm">{error}</p>
              </div>
            {/if}

            <Button type="submit" class="w-full" disabled={loading || !email.trim()}>
              {#if loading}
                <div class="loading-spinner mr-2"></div>
              {/if}
              Send Reset Link
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
