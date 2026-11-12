<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { page } from "$app/state";
  import { authAPI } from "$lib/authAPI";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { ShieldCheck, CheckCircle2 } from "lucide-svelte";
  import AuthorizationDetails from "$lib/components/security/AuthorizationDetails.svelte";
  import {
    authorizationClientName,
    authorizationRemainingSeconds,
    authorizationError,
    normalizeDeviceCode,
    type AuthorizationPreview,
  } from "$lib/auth/consent";

  let code = $state("");
  let previewCode = $state("");
  let preview = $state<AuthorizationPreview | null>(null);
  let lookingUp = $state(false);
  let approving = $state(false);
  let approved = $state(false);
  let error = $state("");
  let now = $state(Date.now());
  let generation = 0;
  const expired = $derived(
    preview ? authorizationRemainingSeconds(preview.expires_at, now) === 0 : false
  );
  const canApprove = $derived(
    !!preview && previewCode === code && !expired && !approving && !lookingUp
  );
  onMount(() => {
    code = normalizeDeviceCode(
      page.url.searchParams.get("user_code") ?? page.url.searchParams.get("code") ?? ""
    );
    if (code.replace("-", "").length === 8) void lookup();
    const timer = setInterval(() => {
      now = Date.now();
    }, 1000);
    return () => clearInterval(timer);
  });
  onDestroy(() => {
    generation++;
  });
  function setCode(value: string) {
    generation++;
    code = normalizeDeviceCode(value);
    preview = null;
    previewCode = "";
    lookingUp = false;
    error = "";
  }
  async function lookup() {
    if (code.replace("-", "").length !== 8 || lookingUp) return;
    const submitted = code;
    const request = ++generation;
    lookingUp = true;
    preview = null;
    previewCode = "";
    error = "";
    try {
      const response = await authAPI.post<AuthorizationPreview>("/device/lookup", {
        user_code: submitted,
      });
      if (request !== generation || submitted !== code) return;
      preview = response.data;
      previewCode = submitted;
    } catch (cause) {
      if (request === generation)
        error = authorizationError(cause, "Could not find this device code.");
    } finally {
      if (request === generation) lookingUp = false;
    }
  }
  async function approve() {
    if (!canApprove) return;
    const submitted = previewCode;
    approving = true;
    error = "";
    try {
      const response = await authAPI.post<{ success: boolean }>("/device/approve", {
        user_code: submitted,
      });
      if (!response.data.success) throw new Error("The sign-in was not approved. Try again.");
      approved = true;
    } catch (cause) {
      error = authorizationError(cause, "Could not approve this sign-in.");
    } finally {
      approving = false;
    }
  }
</script>

<svelte:head><title>Approve Device Sign-in - FrameWorks</title></svelte:head>
<section class="min-h-full bg-background flex items-center justify-center p-4 sm:p-8">
  <div class="slab w-full max-w-xl border border-border">
    <div class="slab-header flex items-center gap-3">
      <ShieldCheck class="w-5 h-5 text-primary" />
      <h1>Approve sign-in</h1>
    </div>
    <div class="slab-body--padded">
      {#if approved}<div class="flex gap-3">
          <CheckCircle2 class="w-7 h-7 text-success shrink-0" />
          <div>
            <p class="font-medium">Sign-in approved</p>
            <p class="mt-2 text-sm text-muted-foreground">
              Return to {preview
                ? authorizationClientName(preview.client_id)
                : "the app that requested this code"} to finish signing in.
            </p>
          </div>
        </div>
      {:else}
        <p class="text-sm text-muted-foreground mb-4">
          Enter the code shown by the app you are signing in to. Review its request before
          approving.
        </p>
        <label for="device-code" class="block text-sm font-medium mb-2">Device code</label><Input
          id="device-code"
          value={code}
          disabled={approving}
          oninput={(event) => setCode(event.currentTarget.value)}
          placeholder="ABCD-EFGH"
          class="font-mono tracking-widest"
          autocomplete="one-time-code"
        />
        {#if preview}<div class="mt-4">
            <h2 class="font-medium">
              Sign in {authorizationClientName(preview.client_id)}{preview.device_label
                ? ` on ${preview.device_label}`
                : ""}
            </h2>
            <AuthorizationDetails request={preview} {now} />
          </div>{/if}
        {#if expired}<p role="alert" class="mt-4 text-sm text-destructive">
            This request expired. Start sign-in again from your app.
          </p>{/if}
        {#if error}<p role="alert" class="mt-4 text-sm text-destructive">{error}</p>{/if}
      {/if}
    </div>
    {#if !approved}<div class="slab-actions">
        {#if preview}<Button variant="ghost" onclick={approve} disabled={!canApprove}
            >{approving ? "Approving..." : "Approve sign-in"}</Button
          >{:else}<Button
            variant="ghost"
            onclick={lookup}
            disabled={lookingUp || code.replace("-", "").length !== 8}
            >{lookingUp ? "Looking up request..." : "Continue"}</Button
          >{/if}
      </div>{/if}
  </div>
</section>
