<script lang="ts">
  import { onMount } from "svelte";
  import { page } from "$app/state";
  import { resolve } from "$app/paths";
  import { AUTH_URL } from "$lib/authAPI.js";
  import { auth } from "$lib/stores/auth.js";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { getIconComponent } from "$lib/iconUtils";

  let verificationStatus = $state<"pending" | "success" | "error">("pending");
  let message = $state("");
  let loading = $state(false);

  // Resend form state
  let resendEmail = $state("");
  let resendLoading = $state(false);
  let resendMessage = $state("");
  let resendSuccess = $state(false);
  let resendCooldown = $state(0);

  // Icons
  const MailIcon = getIconComponent("Mail");
  const CheckCircleIcon = getIconComponent("CheckCircle");
  const AlertTriangleIcon = getIconComponent("AlertTriangle");

  onMount(async () => {
    const token = page.url.searchParams.get("token");

    if (token) {
      // Verify the token
      loading = true;
      try {
        const response = await fetch(`${AUTH_URL}/verify/${encodeURIComponent(token)}`, {
          method: "GET",
        });

        const data = await response.json();

        if (response.ok) {
          verificationStatus = "success";
          message = data.message || "Your email has been verified successfully!";
        } else {
          verificationStatus = "error";
          message = data.error || "Verification failed. The token may be invalid or expired.";
        }
      } catch {
        verificationStatus = "error";
        message = "Network error during verification.";
      } finally {
        loading = false;
      }
    } else {
      // No token, show pending message
      verificationStatus = "pending";
      message = "Please check your email and click the verification link.";
    }
  });

  async function handleResend() {
    if (!resendEmail.trim() || resendCooldown > 0) return;

    resendLoading = true;
    resendMessage = "";
    resendSuccess = false;

    try {
      const result = await auth.resendVerification(resendEmail.trim());

      if (result.success) {
        resendSuccess = true;
        resendMessage = result.error || "Verification email sent! Check your inbox.";
        // Start 60 second cooldown
        resendCooldown = 60;
        const interval = setInterval(() => {
          resendCooldown--;
          if (resendCooldown <= 0) {
            clearInterval(interval);
          }
        }, 1000);
      } else {
        resendSuccess = false;
        resendMessage = result.error || "Failed to send verification email.";
      }
    } catch {
      resendSuccess = false;
      resendMessage = "Network error. Please try again.";
    } finally {
      resendLoading = false;
    }
  }
</script>

<svelte:head>
  <title>Email Verification - FrameWorks</title>
</svelte:head>

<section class="min-h-full bg-brand-surface-muted flex items-center justify-center p-4 sm:p-8">
  <div class="w-full max-w-md">
    {#if loading}
      <!-- Loading State Slab -->
      <div class="slab">
        <div class="slab-header">
          <div class="flex items-center gap-2">
            <div class="loading-spinner w-4 h-4"></div>
            <h3>Verifying</h3>
          </div>
        </div>
        <div class="slab-body--padded text-center">
          <p class="text-muted-foreground">Verifying your email address...</p>
        </div>
      </div>
    {:else if verificationStatus === "success"}
      <!-- Success State Slab -->
      <div class="slab">
        <div class="slab-header">
          <div class="flex items-center gap-2">
            <CheckCircleIcon class="w-4 h-4 text-success" />
            <h3>Email Verified</h3>
          </div>
        </div>
        <div class="slab-body--padded">
          <p class="text-muted-foreground mb-4">{message}</p>
          <p class="text-sm text-muted-foreground">
            Your account is now active. You can sign in to access your dashboard.
          </p>
        </div>
        <div class="slab-actions">
          <Button href={resolve("/login")} class="w-full justify-center">
            Continue to Sign In
          </Button>
        </div>
      </div>
    {:else if verificationStatus === "error"}
      <!-- Error State Slab -->
      <div class="slab">
        <div class="slab-header">
          <div class="flex items-center gap-2">
            <AlertTriangleIcon class="w-4 h-4 text-destructive" />
            <h3>Verification Failed</h3>
          </div>
        </div>
        <div class="slab-body--padded">
          <p class="text-destructive mb-4">{message}</p>

          <!-- Resend verification form -->
          <div class="pt-4 border-t border-border">
            <p class="text-sm text-muted-foreground mb-4">
              Need a new verification link? Enter your email below.
            </p>
            <form
              onsubmit={(e) => {
                e.preventDefault();
                handleResend();
              }}
              class="space-y-3"
            >
              <Input
                type="email"
                placeholder="Enter your email"
                bind:value={resendEmail}
                disabled={resendLoading || resendCooldown > 0}
              />
              <Button
                type="submit"
                variant="outline"
                class="w-full justify-center"
                disabled={resendLoading || resendCooldown > 0 || !resendEmail.trim()}
              >
                {#if resendLoading}
                  Sending...
                {:else if resendCooldown > 0}
                  Resend in {resendCooldown}s
                {:else}
                  Resend Verification Email
                {/if}
              </Button>
            </form>
            {#if resendMessage}
              <p
                class={resendSuccess
                  ? "text-success text-sm mt-2"
                  : "text-destructive text-sm mt-2"}
              >
                {resendMessage}
              </p>
            {/if}
          </div>
        </div>
        <div class="slab-actions">
          <Button href={resolve("/login")} class="w-full justify-center">
            Continue to Sign In
          </Button>
        </div>
      </div>
    {:else}
      <!-- Pending State Slab (awaiting verification) -->
      <div class="slab">
        <div class="slab-header">
          <div class="flex items-center gap-2">
            <MailIcon class="w-4 h-4 text-primary" />
            <h3>Check Your Email</h3>
          </div>
        </div>
        <div class="slab-body--padded">
          <p class="text-muted-foreground mb-4">
            We've sent a verification email to your address. Please click the link in the email to
            verify your account.
          </p>

          <div class="space-y-2 text-sm text-muted-foreground mb-4">
            <p>• Check your spam/junk folder if you don't see the email</p>
            <p>• The verification link expires in 24 hours</p>
            <p>• You'll be able to sign in once your email is verified</p>
          </div>

          <!-- Resend verification form -->
          <div class="pt-4 border-t border-border">
            <p class="text-sm text-muted-foreground mb-4">
              Didn't receive the email? Request a new one.
            </p>
            <form
              onsubmit={(e) => {
                e.preventDefault();
                handleResend();
              }}
              class="space-y-3"
            >
              <Input
                type="email"
                placeholder="Enter your email"
                bind:value={resendEmail}
                disabled={resendLoading || resendCooldown > 0}
              />
              <Button
                type="submit"
                variant="outline"
                class="w-full justify-center"
                disabled={resendLoading || resendCooldown > 0 || !resendEmail.trim()}
              >
                {#if resendLoading}
                  Sending...
                {:else if resendCooldown > 0}
                  Resend in {resendCooldown}s
                {:else}
                  Resend Verification Email
                {/if}
              </Button>
            </form>
            {#if resendMessage}
              <p
                class={resendSuccess
                  ? "text-success text-sm mt-2"
                  : "text-destructive text-sm mt-2"}
              >
                {resendMessage}
              </p>
            {/if}
          </div>
        </div>
      </div>
    {/if}
  </div>
</section>
