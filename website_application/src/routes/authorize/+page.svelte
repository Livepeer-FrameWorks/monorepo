<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { page } from "$app/state";
  import { resolve } from "$app/paths";
  import { authAPI } from "$lib/authAPI";
  import { Button } from "$lib/components/ui/button";
  import { ShieldCheck } from "lucide-svelte";
  import AuthorizationDetails from "$lib/components/security/AuthorizationDetails.svelte";
  import {
    authorizationClientName,
    authorizationRemainingSeconds,
    authorizationError,
    loopbackAuthorizationRedirect,
    returnToAuthorizationClient,
    type AuthorizationPreview,
  } from "$lib/auth/consent";

  let preview = $state<AuthorizationPreview | null>(null);
  let verifiedContext = $state("");
  let lookingUp = $state(false);
  let approving = $state(false);
  let completed = $state(false);
  let error = $state("");
  let mounted = $state(false);
  let now = $state(Date.now());
  let generation = 0;
  const params = $derived(page.url.searchParams);
  const requestContext = $derived(params.get("request_context") ?? "");
  const legacyRequest = $derived<AuthorizationPreview>({
    client_id: params.get("client_id") ?? "",
    redirect_uri: params.get("redirect_uri") ?? "",
    code_challenge: params.get("code_challenge") ?? "",
    code_challenge_method: params.get("code_challenge_method") ?? "",
    scope: params.get("scope") ?? "account",
    state: params.get("state") ?? "",
  });
  const request = $derived(requestContext ? preview : legacyRequest);
  const redirectAllowed = $derived(
    request?.client_id === "tray-mac" && loopbackAuthorizationRedirect(request.redirect_uri ?? "")
  );
  const valid = $derived(
    !!request &&
      redirectAllowed &&
      !!request.code_challenge &&
      request.code_challenge_method === "S256" &&
      (!requestContext || verifiedContext === requestContext)
  );
  const expired = $derived(
    request ? authorizationRemainingSeconds(request.expires_at, now) === 0 : false
  );
  const canApprove = $derived(valid && !lookingUp && !approving && !expired && !completed);
  onMount(() => {
    mounted = true;
    const timer = setInterval(() => {
      now = Date.now();
    }, 1000);
    return () => clearInterval(timer);
  });
  onDestroy(() => {
    generation++;
  });
  $effect(() => {
    if (mounted) void lookup(requestContext);
  });
  async function lookup(token: string) {
    const current = ++generation;
    preview = null;
    verifiedContext = "";
    completed = false;
    error = "";
    if (!token) {
      lookingUp = false;
      return;
    }
    lookingUp = true;
    try {
      const response = await authAPI.post<AuthorizationPreview>("/authorize/lookup", {
        request_context: token,
      });
      if (current !== generation) return;
      if (!response.data.client_id || !response.data.redirect_uri || !response.data.code_challenge)
        throw new Error("Could not verify this sign-in request. Start again from your app.");
      preview = response.data;
      verifiedContext = token;
    } catch (cause) {
      if (current === generation)
        error = authorizationError(
          cause,
          "Could not verify this sign-in request. Start again from your app."
        );
    } finally {
      if (current === generation) lookingUp = false;
    }
  }
  function deny() {
    if (!redirectAllowed || !request || completed || approving) return;
    const target = new URL(request.redirect_uri!);
    target.searchParams.set("error", "access_denied");
    if (request.state) target.searchParams.set("state", request.state);
    returnToAuthorizationClient(target);
    completed = true;
  }
  async function approve() {
    if (!canApprove || !request) return;
    const submitted = request;
    const context = requestContext;
    approving = true;
    error = "";
    try {
      const response = await authAPI.post<{ code: string }>("/authorize/complete", {
        client_id: submitted.client_id,
        redirect_uri: submitted.redirect_uri,
        code_challenge: submitted.code_challenge,
        code_challenge_method: submitted.code_challenge_method,
        scope: submitted.scope,
        state: submitted.state,
        ...(context ? { request_context: context } : {}),
      });
      if (!response.data.code)
        throw new Error("The service did not return an approval code. Try again.");
      const target = new URL(submitted.redirect_uri!);
      target.searchParams.set("code", response.data.code);
      if (submitted.state) target.searchParams.set("state", submitted.state);
      returnToAuthorizationClient(target);
      completed = true;
    } catch (cause) {
      error = authorizationError(cause, "Could not approve this sign-in.");
    } finally {
      approving = false;
    }
  }
</script>

<svelte:head><title>Approve Sign-in - FrameWorks</title></svelte:head>
<section class="min-h-full bg-background flex items-center justify-center p-4 sm:p-8">
  <div class="slab w-full max-w-xl border border-border">
    <div class="slab-header flex items-center gap-3">
      <ShieldCheck class="w-5 h-5 text-primary" />
      <h1>Approve sign-in</h1>
    </div>
    <div class="slab-body--padded">
      {#if lookingUp}<p role="status" class="text-sm text-muted-foreground">
          Verifying sign-in request...
        </p>
      {:else if valid && request}
        <h2 class="font-medium">
          Sign in {authorizationClientName(request.client_id)}{request.device_label
            ? ` on ${request.device_label}`
            : ""}
        </h2>
        <AuthorizationDetails {request} {now} />
        {#if !requestContext}<p class="mt-4 text-sm text-muted-foreground">
            Device details and request expiry are unavailable for this sign-in. Confirm it in the
            app where you started.
          </p>{/if}
        {#if expired}<p role="alert" class="mt-4 text-sm text-destructive">
            This request expired. Start sign-in again from your app.
          </p>{/if}
        {#if completed}<p role="status" class="mt-4 text-sm">
            Return to the requesting app to continue.
          </p>{/if}
      {:else if !error}<p role="alert" class="text-sm text-destructive">
          This sign-in request is invalid or missing required information. Start again from your
          app.
        </p>{/if}
      {#if error}<p role="alert" class="mt-4 text-sm text-destructive">{error}</p>{/if}
    </div>
    <div class="slab-actions slab-actions--row">
      {#if redirectAllowed}<Button variant="ghost" onclick={deny} disabled={approving || completed}
          >Deny</Button
        >{:else}<Button variant="ghost" href={resolve("/")}>Back to account</Button>{/if}<Button
        variant="ghost"
        onclick={approve}
        disabled={!canApprove}>{approving ? "Approving..." : "Approve sign-in"}</Button
      >
    </div>
  </div>
</section>
