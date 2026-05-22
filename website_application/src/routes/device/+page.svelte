<script lang="ts">
  import { onMount } from "svelte";
  import { page } from "$app/stores";
  import { authAPI } from "$lib/authAPI";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Alert, AlertDescription } from "$lib/components/ui/alert";
  import { CheckCircle2, KeyRound, ShieldCheck, Terminal } from "lucide-svelte";

  let userCode = $state("");
  let lookingUp = $state(false);
  let approving = $state(false);
  let approved = $state(false);
  let clientId = $state("");
  let scope = $state("");
  let error = $state("");

  onMount(() => {
    userCode = normalizeCode(
      $page.url.searchParams.get("user_code") ?? $page.url.searchParams.get("code") ?? ""
    );
    if (userCode.replace("-", "").length === 8) {
      void lookup();
    }
  });

  function normalizeCode(value: string): string {
    // Server emits user_codes from the Crockford alphabet (0-9, A-Z minus
    // I/L/O/U). Accept any alphanumeric input and let the server reject
    // characters that can't appear in a real code. A stricter [A-Z2-7]
    // base32 regex would silently strip legitimate digits like 0/1/8/9.
    return value
      .toUpperCase()
      .replace(/[^A-Z0-9]/g, "")
      .slice(0, 8)
      .replace(/^(.{4})(.+)$/, "$1-$2");
  }

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

  const clientName = $derived(clientId === "cli" ? "FrameWorks CLI" : clientId);
  const hasPreview = $derived(clientId.length > 0);

  function setUserCode(value: string) {
    userCode = normalizeCode(value);
    clientId = "";
    scope = "";
    error = "";
  }

  async function lookup() {
    if (lookingUp || userCode.replace("-", "").length !== 8) return;

    lookingUp = true;
    error = "";
    clientId = "";
    scope = "";
    try {
      const response = await authAPI.post<{
        client_id: string;
        scope: string;
        expires_at?: string;
      }>("/device/lookup", {
        user_code: userCode,
      });
      clientId = response.data.client_id;
      scope = response.data.scope;
    } catch (err) {
      error = axiosErrorMessage(err, "Could not find this device code.");
    } finally {
      lookingUp = false;
    }
  }

  async function approve() {
    if (approving || !hasPreview || userCode.replace("-", "").length !== 8) return;

    approving = true;
    error = "";
    try {
      const response = await authAPI.post<{ success: boolean; client_id: string }>(
        "/device/approve",
        {
          user_code: userCode,
        }
      );
      approved = response.data.success;
    } catch (err) {
      error = axiosErrorMessage(err, "Could not approve this device code.");
    } finally {
      approving = false;
    }
  }
</script>

<svelte:head>
  <title>Device Login - FrameWorks</title>
</svelte:head>

<section class="min-h-full bg-background flex items-center justify-center p-4 sm:p-8">
  <div class="w-full max-w-lg border border-border bg-card rounded-lg shadow-sm overflow-hidden">
    <div class="px-5 py-4 border-b border-border flex items-center gap-3">
      <Terminal class="w-5 h-5 text-primary" />
      <h1 class="text-lg font-semibold text-foreground">Authorize CLI</h1>
    </div>

    <div class="p-5 space-y-4">
      {#if approved}
        <div class="flex items-start gap-3">
          <CheckCircle2 class="w-8 h-8 text-success shrink-0 mt-1" />
          <div>
            <p class="text-base font-medium text-foreground">CLI authorized</p>
            <p class="mt-2 text-sm text-muted-foreground">
              You can return to the terminal. The CLI will finish as soon as its next poll
              completes.
            </p>
            {#if clientId}
              <p class="mt-2 text-xs text-muted-foreground">
                Client: <span class="font-mono text-foreground">{clientId}</span>
              </p>
            {/if}
          </div>
        </div>
      {:else}
        <div class="flex items-start gap-3">
          <KeyRound class="w-8 h-8 text-primary shrink-0 mt-1" />
          <div>
            <p class="text-base font-medium text-foreground">Enter the code from your terminal</p>
            <p class="mt-2 text-sm text-muted-foreground">
              We will show the requesting client before you authorize the session.
            </p>
          </div>
        </div>

        <div>
          <label for="device-code" class="block text-sm font-medium text-muted-foreground mb-2">
            Device Code
          </label>
          <Input
            id="device-code"
            value={userCode}
            oninput={(event) => {
              setUserCode((event.currentTarget as HTMLInputElement).value);
            }}
            placeholder="ABCD-EFGH"
            class="font-mono text-center tracking-widest"
          />
        </div>

        {#if hasPreview}
          <div
            class="rounded-md bg-muted/40 border border-border px-3 py-2 text-xs text-muted-foreground"
          >
            <div class="flex justify-between gap-3">
              <span>Client</span>
              <span class="font-mono text-foreground">{clientName}</span>
            </div>
            <div class="flex justify-between gap-3 mt-1">
              <span>Scope</span>
              <span class="font-mono text-foreground">{scope || "account"}</span>
            </div>
          </div>
        {/if}

        {#if error}
          <Alert variant="destructive">
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        {/if}
      {/if}
    </div>

    {#if !approved}
      <div class="px-5 py-4 border-t border-border flex justify-end">
        {#if hasPreview}
          <Button
            class="gap-2"
            onclick={approve}
            disabled={approving || userCode.replace("-", "").length !== 8}
          >
            {#if approving}
              <div class="loading-spinner"></div>
            {:else}
              <CheckCircle2 class="w-4 h-4" />
            {/if}
            Authorize CLI
          </Button>
        {:else}
          <Button
            class="gap-2"
            onclick={lookup}
            disabled={lookingUp || userCode.replace("-", "").length !== 8}
          >
            {#if lookingUp}
              <div class="loading-spinner"></div>
            {:else}
              <ShieldCheck class="w-4 h-4" />
            {/if}
            Continue
          </Button>
        {/if}
      </div>
    {/if}
  </div>
</section>
