<script>
  import { onMount } from "svelte";
  import { page } from "$app/stores";
  import { base } from "$app/paths";
  import { AUTH_URL } from "$lib/authAPI.js";

  let verificationStatus = "pending"; // pending, success, error
  let message = "";
  let loading = false;

  onMount(async () => {
    const token = $page.url.searchParams.get("token");

    if (token) {
      // Verify the token
      loading = true;
      try {
        const response = await fetch(
          `${AUTH_URL}/verify/${encodeURIComponent(token)}`,
          {
            method: "GET",
          }
        );

        const data = await response.json();

        if (response.ok) {
          verificationStatus = "success";
          message =
            data.message || "Your email has been verified successfully!";
        } else {
          verificationStatus = "error";
          message =
            data.error ||
            "Verification failed. The token may be invalid or expired.";
        }
      } catch (error) {
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
</script>

<svelte:head>
  <title>Email Verification - FrameWorks</title>
</svelte:head>

<div class="min-h-screen flex justify-center items-center">
  <div class="max-w-md w-full space-y-8">
    <div class="text-center">
      <div class="flex justify-center mb-4">
        <img src="/frameworks-dark-logomark-transparent.svg" alt="FrameWorks" class="h-32 w-32 rounded-lg" />
      </div>
      <h1 class="text-4xl font-bold gradient-text mb-2">
        {#if verificationStatus === "success"}
          Email Verified!
        {:else if verificationStatus === "error"}
          Verification Failed
        {:else}
          Check Your Email
        {/if}
      </h1>
    </div>

    <div class="glow-card p-8 text-center">
      {#if loading}
        <div class="flex justify-center items-center space-y-4">
          <div class="loading-spinner mr-2" />
          <p class="text-tokyo-night-fg-dark">Verifying your email...</p>
        </div>
      {:else if verificationStatus === "success"}
        <div class="space-y-4">
          <div class="flex justify-center">
            <svg
              class="w-16 h-16 text-tokyo-night-green"
              fill="none"
              stroke="currentColor"
              viewBox="0 0 24 24"
            >
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                stroke-width="2"
                d="M9 12l2 2 4-4m6 2a9 9 0 11-18 0 9 9 0 0118 0z"
              />
            </svg>
          </div>
          <p class="text-tokyo-night-fg-dark">{message}</p>
          <div class="mt-6">
            <a href="{base}/login" class="btn-primary"> Continue to Sign In </a>
          </div>
        </div>
      {:else if verificationStatus === "error"}
        <div class="space-y-4">
          <div class="flex justify-center">
            <svg
              class="w-16 h-16 text-tokyo-night-red"
              fill="none"
              stroke="currentColor"
              viewBox="0 0 24 24"
            >
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                stroke-width="2"
                d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-2.5L13.732 4c-.77-.833-1.964-.833-2.732 0L3.732 16.5c-.77.833.192 2.5 1.732 2.5z"
              />
            </svg>
          </div>
          <p class="text-tokyo-night-red">{message}</p>
          <div class="mt-6 space-y-3">
            <a href="{base}/register" class="btn-secondary block text-center">
              Try Registering Again
            </a>
            <a href="{base}/login" class="btn-primary block text-center">
              Continue to Sign In
            </a>
          </div>
        </div>
      {:else}
        <div class="space-y-4">
          <div class="flex justify-center">
            <svg
              class="w-16 h-16 text-tokyo-night-blue"
              fill="none"
              stroke="currentColor"
              viewBox="0 0 24 24"
            >
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                stroke-width="2"
                d="M3 8l7.89 4.26a2 2 0 002.22 0L21 8M5 19h14a2 2 0 002-2V7a2 2 0 00-2-2H5a2 2 0 00-2 2v10a2 2 0 002 2z"
              />
            </svg>
          </div>
          <h2 class="text-xl font-semibold text-tokyo-night-fg-dark">
            Registration Successful!
          </h2>
          <p class="text-tokyo-night-fg-dark">
            We've sent a verification email to your address. Please click the
            link in the email to verify your account and complete registration.
          </p>
          <div class="mt-6 text-sm text-tokyo-night-comment space-y-2">
            <p>• Check your spam/junk folder if you don't see the email</p>
            <p>• The verification link expires in 24 hours</p>
            <p>• You'll be able to sign in once your email is verified</p>
          </div>
        </div>
      {/if}
    </div>
  </div>
</div>
