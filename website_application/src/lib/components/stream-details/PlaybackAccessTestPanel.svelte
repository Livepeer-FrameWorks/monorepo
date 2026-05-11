<script lang="ts">
  import { TestPlaybackAccessStore } from "$houdini";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Label } from "$lib/components/ui/label";
  import { toast } from "$lib/stores/toast";
  import { ShieldQuestion, Webhook, Send, AlertTriangle } from "lucide-svelte";

  // Operator-facing playback policy tester. Runs the same evaluator that
  // Foghorn's USER_NEW handler uses, but against a caller-supplied JWT (or
  // a webhook test) without registering a viewer session.
  //
  // Webhook mode fires a real outbound HTTPS request to the customer URL;
  // the "Send test webhook request" button is the explicit opt-in for
  // that side effect. JWT mode is side-effect free.
  //
  // The token never leaves the panel — we don't persist it in localStorage,
  // and the toast / decision summary intentionally does not echo it back.

  interface Props {
    playbackId: string;
    policyType: "PUBLIC" | "JWT" | "WEBHOOK";
  }

  let { playbackId, policyType }: Props = $props();

  const testMutation = new TestPlaybackAccessStore();

  let token = $state("");
  let viewerIp = $state("");
  let connector = $state("hls");
  let pending = $state(false);

  type Decision = {
    allowed: boolean;
    policyType: string;
    reason?: string | null;
    detail?: string | null;
    kid?: string | null;
    claimsJson?: string | null;
    webhookStatus?: number | null;
    webhookLatencyMs?: number | null;
  };
  let decision = $state<Decision | null>(null);
  let mode = $derived<"jwt" | "webhook" | "none">(
    policyType === "JWT" ? "jwt" : policyType === "WEBHOOK" ? "webhook" : "none"
  );

  async function runTest(fireWebhook: boolean) {
    if (mode === "jwt" && !token.trim()) {
      toast.error("Paste a token to test JWT policy");
      return;
    }
    pending = true;
    decision = null;
    try {
      const result = await testMutation.mutate({
        input: {
          playbackId,
          viewerToken: token.trim() || undefined,
          viewerIp: viewerIp.trim() || undefined,
          connector: connector.trim() || undefined,
          fireWebhook,
        },
      });
      const data = result.data?.testPlaybackAccess;
      if (!data) {
        toast.error("No response from server");
        return;
      }
      switch (data.__typename) {
        case "PlaybackAccessDecision":
          decision = data;
          if (fireWebhook) {
            toast.success(data.allowed ? "Webhook allowed" : "Webhook denied — see detail");
          }
          break;
        case "ValidationError":
          toast.error(`${data.message}${data.field ? ` (${data.field})` : ""}`);
          break;
        case "NotFoundError":
          toast.error(data.message || "Playback target not found");
          break;
        case "AuthError":
          toast.error(data.message || "Not authorised");
          break;
      }
    } catch (err) {
      toast.error(`Test failed: ${(err as Error).message}`);
    } finally {
      pending = false;
    }
  }

  function decisionBadge(d: Decision): string {
    return d.allowed ? "bg-success/15 text-success" : "bg-destructive/15 text-destructive";
  }

  // Pretty-print claims JSON if present; fall back to raw on parse error so
  // operators can see malformed payloads.
  function prettyClaims(s: string | null | undefined): string {
    if (!s) return "";
    try {
      return JSON.stringify(JSON.parse(s), null, 2);
    } catch {
      return s;
    }
  }
</script>

<section class="border border-border rounded-md p-4 mt-6">
  <div class="flex items-center gap-2 mb-1">
    <ShieldQuestion class="w-4 h-4 text-primary" />
    <h3 class="text-base font-semibold">Test access</h3>
  </div>
  <p class="text-xs text-muted-foreground mb-4">
    Dry-run the current policy without registering a viewer session. JWT mode is side-effect free;
    webhook mode (below) requires explicit confirmation because it calls your endpoint.
  </p>

  {#if mode === "none"}
    <p class="text-xs text-muted-foreground italic">
      Stream is set to public — there's nothing to test. Switch to JWT or Webhook above to enable.
    </p>
  {:else}
    <div class="space-y-3 mb-3">
      {#if mode === "jwt"}
        <div>
          <Label for="test-token" class="text-xs">JWT to test</Label>
          <Input
            id="test-token"
            type="text"
            placeholder="eyJhbGciOiJFUzI1NiIs..."
            bind:value={token}
            disabled={pending}
            autocomplete="off"
            spellcheck="false"
          />
        </div>
      {/if}
      <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
        <div>
          <Label for="test-viewer-ip" class="text-xs">Viewer IP (optional)</Label>
          <Input
            id="test-viewer-ip"
            placeholder="203.0.113.42"
            bind:value={viewerIp}
            disabled={pending}
          />
        </div>
        <div>
          <Label for="test-connector" class="text-xs">Connector (optional)</Label>
          <Input id="test-connector" placeholder="hls" bind:value={connector} disabled={pending} />
        </div>
      </div>
    </div>

    {#if mode === "webhook"}
      <div
        class="border border-warning/30 bg-warning/5 rounded p-2 mb-3 text-xs flex items-start gap-2"
      >
        <AlertTriangle class="w-3.5 h-3.5 text-warning shrink-0 mt-0.5" />
        <div>
          Webhook mode sends a real <strong>HMAC-signed POST</strong> to your configured URL with the
          fields above. Use this to confirm your endpoint accepts the signature and returns 200.
        </div>
      </div>
    {/if}

    <div class="flex flex-wrap gap-2 mb-3">
      {#if mode === "jwt"}
        <Button onclick={() => runTest(false)} disabled={pending || !token.trim()}>
          <Send class="w-4 h-4 mr-1" /> Test JWT
        </Button>
      {/if}
      {#if mode === "webhook"}
        <Button variant="destructive" onclick={() => runTest(true)} disabled={pending}>
          <Webhook class="w-4 h-4 mr-1" /> Send test webhook request
        </Button>
        <Button variant="outline" onclick={() => runTest(false)} disabled={pending}>
          Inspect policy (no call)
        </Button>
      {/if}
    </div>

    {#if decision}
      <div class="border border-border rounded p-3 space-y-2">
        <div class="flex items-center gap-2">
          <span class="text-xs px-2 py-0.5 rounded {decisionBadge(decision)}">
            {decision.allowed ? "ALLOW" : "DENY"}
          </span>
          <span class="text-xs text-muted-foreground">policy: {decision.policyType || "—"}</span>
        </div>
        {#if decision.reason}
          <div class="text-xs">
            <span class="text-muted-foreground">reason:</span>
            <code class="font-mono">{decision.reason}</code>
          </div>
        {/if}
        {#if decision.detail}
          <div class="text-xs">
            <span class="text-muted-foreground">detail:</span>
            <code class="font-mono break-all">{decision.detail}</code>
          </div>
        {/if}
        {#if decision.kid}
          <div class="text-xs">
            <span class="text-muted-foreground">kid:</span>
            <code class="font-mono">{decision.kid}</code>
          </div>
        {/if}
        {#if decision.webhookStatus !== null && decision.webhookStatus !== undefined && decision.webhookStatus > 0}
          <div class="text-xs">
            <span class="text-muted-foreground">webhook:</span>
            <code class="font-mono">HTTP {decision.webhookStatus}</code>
            <span class="text-muted-foreground">in {decision.webhookLatencyMs ?? 0}ms</span>
          </div>
        {/if}
        {#if decision.claimsJson}
          <details class="text-xs">
            <summary class="cursor-pointer text-muted-foreground">JWT claims (parsed)</summary>
            <pre
              class="font-mono text-[10px] mt-1 p-2 bg-muted/50 rounded overflow-x-auto">{prettyClaims(
                decision.claimsJson
              )}</pre>
          </details>
        {/if}
      </div>
    {/if}
  {/if}
</section>
