<script lang="ts">
  import { onMount } from "svelte";
  import { page } from "$app/stores";
  import { authAPI } from "$lib/authAPI";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Alert, AlertDescription } from "$lib/components/ui/alert";
  import { CheckCircle2, KeyRound, Terminal } from "lucide-svelte";

  let userCode = $state("");
  let approving = $state(false);
  let approved = $state(false);
  let clientId = $state("");
  let error = $state("");

  onMount(() => {
    userCode = normalizeCode(
      $page.url.searchParams.get("user_code") ?? $page.url.searchParams.get("code") ?? ""
    );
  });

  function normalizeCode(value: string): string {
    return value
      .toUpperCase()
      .replace(/[^A-Z2-7]/g, "")
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

  async function approve() {
    if (approving || userCode.replace("-", "").length !== 8) return;

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
      clientId = response.data.client_id;
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
              Approving this code grants a FrameWorks account session to the CLI that requested it.
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
              userCode = normalizeCode((event.currentTarget as HTMLInputElement).value);
            }}
            placeholder="ABCD-EFGH"
            class="font-mono text-center tracking-widest"
          />
        </div>

        {#if error}
          <Alert variant="destructive">
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        {/if}
      {/if}
    </div>

    {#if !approved}
      <div class="px-5 py-4 border-t border-border flex justify-end">
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
      </div>
    {/if}
  </div>
</section>
