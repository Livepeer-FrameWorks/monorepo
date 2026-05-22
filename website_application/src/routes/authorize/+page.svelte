<script lang="ts">
  import { page } from "$app/stores";
  import { authAPI } from "$lib/authAPI";
  import { Button } from "$lib/components/ui/button";
  import { Alert, AlertDescription } from "$lib/components/ui/alert";
  import { MonitorCheck, ShieldCheck, XCircle } from "lucide-svelte";

  let approving = $state(false);
  let error = $state("");

  const params = $derived($page.url.searchParams);
  const clientId = $derived(params.get("client_id") ?? "");
  const redirectURI = $derived(params.get("redirect_uri") ?? "");
  const codeChallenge = $derived(params.get("code_challenge") ?? "");
  const codeChallengeMethod = $derived(params.get("code_challenge_method") ?? "");
  const scope = $derived(params.get("scope") ?? "account");
  const requestState = $derived(params.get("state") ?? "");

  const clientName = $derived(clientId === "tray-mac" ? "FrameWorks macOS tray" : clientId);

  // Mirror of the server-side check in validateAuthorizationClient. Done
  // client-side too so the Deny path (which doesn't hit the server) can't be
  // turned into an open redirect to an arbitrary URL via a crafted link.
  function isLoopbackRedirect(uri: string): boolean {
    try {
      const u = new URL(uri);
      if (u.protocol !== "http:") return false;
      if (u.hostname !== "127.0.0.1" && u.hostname !== "[::1]" && u.hostname !== "::1")
        return false;
      if (u.pathname !== "/callback") return false;
      return true;
    } catch {
      return false;
    }
  }

  const redirectAllowed = $derived(
    clientId === "tray-mac" ? isLoopbackRedirect(redirectURI) : false
  );
  const requestValid = $derived(
    clientId.length > 0 &&
      redirectURI.length > 0 &&
      codeChallenge.length > 0 &&
      codeChallengeMethod === "S256" &&
      redirectAllowed
  );

  function axiosErrorMessage(err: unknown, fallback: string): string {
    if (
      err &&
      typeof err === "object" &&
      "response" in err &&
      err.response &&
      typeof err.response === "object" &&
      "data" in err.response &&
      err.response.data &&
      typeof err.response.data === "object" &&
      "error" in err.response.data &&
      typeof err.response.data.error === "string"
    ) {
      return err.response.data.error;
    }
    return fallback;
  }

  function redirectWithError() {
    // Refuse to navigate to a redirect_uri the server would reject. The Deny
    // path doesn't hit the API, so this is the only thing standing between
    // an attacker-crafted ?redirect_uri=https://evil.com and a real bounce.
    if (!redirectAllowed) return;
    const target = new URL(redirectURI);
    target.searchParams.set("error", "access_denied");
    if (requestState) {
      target.searchParams.set("state", requestState);
    }
    window.location.assign(target.toString());
  }

  async function approve() {
    if (!requestValid || approving) return;

    approving = true;
    error = "";
    try {
      const response = await authAPI.post<{ code: string }>("/authorize/complete", {
        client_id: clientId,
        redirect_uri: redirectURI,
        code_challenge: codeChallenge,
        code_challenge_method: codeChallengeMethod,
        scope,
        state: requestState,
      });

      const target = new URL(redirectURI);
      target.searchParams.set("code", response.data.code);
      if (requestState) {
        target.searchParams.set("state", requestState);
      }
      window.location.assign(target.toString());
    } catch (err) {
      error = axiosErrorMessage(err, "Could not authorize this client.");
    } finally {
      approving = false;
    }
  }
</script>

<svelte:head>
  <title>Authorize - FrameWorks</title>
</svelte:head>

<section class="min-h-full bg-background flex items-center justify-center p-4 sm:p-8">
  <div class="w-full max-w-lg border border-border bg-card rounded-lg shadow-sm overflow-hidden">
    <div class="px-5 py-4 border-b border-border flex items-center gap-3">
      <ShieldCheck class="w-5 h-5 text-primary" />
      <h1 class="text-lg font-semibold text-foreground">Authorize Client</h1>
    </div>

    <div class="p-5 space-y-4">
      {#if !requestValid}
        <Alert variant="destructive">
          <AlertDescription>
            This authorization request is missing required PKCE parameters.
          </AlertDescription>
        </Alert>
      {:else}
        <div class="flex items-start gap-3">
          <MonitorCheck class="w-8 h-8 text-primary shrink-0 mt-1" />
          <div>
            <p class="text-sm text-muted-foreground">Allow</p>
            <p class="text-base font-medium text-foreground">{clientName}</p>
            <p class="mt-2 text-sm text-muted-foreground">
              This grants a FrameWorks account session to the native client on this machine.
            </p>
          </div>
        </div>

        <div
          class="rounded-md bg-muted/40 border border-border px-3 py-2 text-xs text-muted-foreground"
        >
          <div class="flex justify-between gap-3">
            <span>Scope</span>
            <span class="font-mono text-foreground">{scope}</span>
          </div>
          <div class="flex justify-between gap-3 mt-1">
            <span>Client</span>
            <span class="font-mono text-foreground">{clientId}</span>
          </div>
        </div>

        {#if error}
          <Alert variant="destructive">
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        {/if}
      {/if}
    </div>

    <div class="px-5 py-4 border-t border-border flex justify-end gap-2">
      <Button variant="outline" class="gap-2" onclick={redirectWithError}>
        <XCircle class="w-4 h-4" />
        Deny
      </Button>
      <Button class="gap-2" onclick={approve} disabled={!requestValid || approving}>
        {#if approving}
          <div class="loading-spinner"></div>
        {:else}
          <ShieldCheck class="w-4 h-4" />
        {/if}
        Authorize
      </Button>
    </div>
  </div>
</section>
