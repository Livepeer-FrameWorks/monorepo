<script lang="ts">
  import { resolve } from "$app/paths";
  import { onMount } from "svelte";
  import {
    GetSigningKeysConnectionStore,
    SetPlaybackPolicyStore,
    type PlaybackPolicyFields$data,
  } from "$houdini";
  import { toast } from "$lib/stores/toast.js";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Badge } from "$lib/components/ui/badge";
  import { Alert, AlertDescription } from "$lib/components/ui/alert";
  import { Select, SelectTrigger, SelectContent, SelectItem } from "$lib/components/ui/select";
  import { ShieldCheck, Globe, Key as KeyIcon, Webhook, Trash2, Plus } from "lucide-svelte";
  import PlaybackAccessTestPanel from "./PlaybackAccessTestPanel.svelte";

  type PolicyType = "PUBLIC" | "JWT" | "WEBHOOK";

  interface ClaimReq {
    name: string;
    jsonValue: string;
  }

  let {
    streamId,
    playbackId = "",
    playbackPolicy = null,
    onSaved,
  }: {
    streamId: string;
    playbackId?: string;
    playbackPolicy?: PlaybackPolicyFields$data | null;
    onSaved?: () => void;
  } = $props();

  const keysStore = new GetSigningKeysConnectionStore();
  const setPolicyMutation = new SetPlaybackPolicyStore();

  let saving = $state(false);

  let policyType = $state<PolicyType>("PUBLIC");
  let allowedKids = $state<string[]>([]);
  let requiredAudience = $state<string[]>([]);
  let requiredClaims = $state<ClaimReq[]>([]);
  let webhookUrl = $state("");
  let webhookSecret = $state("");
  let webhookTimeoutMs = $state("5000");
  let webhookHasExistingSecret = $derived(
    policyType === "WEBHOOK" && playbackPolicy?.type === "WEBHOOK"
  );

  let signingKeys = $derived(
    ($keysStore.data?.signingKeysConnection?.edges ?? [])
      .map((e) => e.node)
      .filter((k) => k.status === "ACTIVE")
  );

  function syncFromPolicy(p: PlaybackPolicyFields$data | null | undefined) {
    if (!p) {
      policyType = "PUBLIC";
      allowedKids = [];
      requiredAudience = [];
      requiredClaims = [];
      webhookUrl = "";
      webhookSecret = "";
      webhookTimeoutMs = "5000";
      return;
    }
    policyType = p.type as PolicyType;
    allowedKids = p.jwt?.allowedKids ? [...p.jwt.allowedKids] : [];
    requiredAudience = p.jwt?.requiredAudience ? [...p.jwt.requiredAudience] : [];
    requiredClaims = (p.jwt?.requiredClaimsJson ?? []).map((c) => ({
      name: c.name,
      jsonValue: c.jsonValue,
    }));
    webhookUrl = p.webhook?.url ?? "";
    webhookSecret = "";
    webhookTimeoutMs = String(p.webhook?.timeoutMs ?? 5000);
  }

  $effect(() => {
    syncFromPolicy(playbackPolicy);
  });

  onMount(async () => {
    try {
      await keysStore.fetch({ variables: { status: "active" } });
    } catch (err) {
      console.error("Failed to load signing keys:", err);
    }
  });

  function toggleKid(kid: string) {
    if (allowedKids.includes(kid)) {
      allowedKids = allowedKids.filter((k) => k !== kid);
    } else {
      allowedKids = [...allowedKids, kid];
    }
  }

  function addAudience() {
    requiredAudience = [...requiredAudience, ""];
  }

  function removeAudience(i: number) {
    requiredAudience = requiredAudience.filter((_, idx) => idx !== i);
  }

  function addClaim() {
    requiredClaims = [...requiredClaims, { name: "", jsonValue: "" }];
  }

  function removeClaim(i: number) {
    requiredClaims = requiredClaims.filter((_, idx) => idx !== i);
  }

  async function savePolicy() {
    if (saving) return;

    if (policyType === "WEBHOOK") {
      if (!webhookUrl.trim()) {
        toast.warning("Webhook URL is required");
        return;
      }
      if (!webhookHasExistingSecret && !webhookSecret.trim()) {
        toast.warning("Webhook secret is required");
        return;
      }
    }

    const cleanAudience = requiredAudience.map((a) => a.trim()).filter(Boolean);
    const cleanClaims = requiredClaims
      .map((c) => ({ name: c.name.trim(), jsonValue: c.jsonValue.trim() }))
      .filter((c) => c.name && c.jsonValue);

    const policy: {
      type: PolicyType;
      jwt?: {
        allowedKids: string[];
        requiredAudience: string[];
        requiredClaimsJson: ClaimReq[];
      };
      webhook?: { url: string; secret?: string; timeoutMs: number };
    } = { type: policyType };

    if (policyType === "JWT") {
      policy.jwt = {
        allowedKids,
        requiredAudience: cleanAudience,
        requiredClaimsJson: cleanClaims,
      };
    } else if (policyType === "WEBHOOK") {
      const trimmedSecret = webhookSecret.trim();
      policy.webhook = {
        url: webhookUrl.trim(),
        timeoutMs: Number(webhookTimeoutMs),
      };
      if (trimmedSecret) {
        policy.webhook.secret = trimmedSecret;
      }
    }

    try {
      saving = true;
      const result = await setPolicyMutation.mutate({
        input: {
          streamId,
          policy: policy as unknown as Parameters<
            typeof setPolicyMutation.mutate
          >[0]["input"]["policy"],
        },
      });

      const data = result.data?.setPlaybackPolicy;
      if (!data) {
        toast.error("Failed to save policy");
        return;
      }
      if (
        data.__typename === "Stream" ||
        data.__typename === "VodAsset" ||
        data.__typename === "Clip"
      ) {
        toast.success("Playback policy saved");
        webhookSecret = "";
        onSaved?.();
      } else {
        const err = data as { message?: string };
        toast.error(err.message || "Failed to save policy");
      }
    } catch (err) {
      console.error("Failed to save playback policy:", err);
      toast.error("Failed to save playback policy. Please try again.");
    } finally {
      saving = false;
    }
  }
</script>

<div class="p-4 sm:p-6 space-y-6">
  <div class="flex items-start justify-between gap-4">
    <div>
      <h3 class="text-base font-semibold text-foreground flex items-center gap-2">
        <ShieldCheck class="w-4 h-4 text-primary" />
        Playback Authentication
      </h3>
      <p class="text-sm text-muted-foreground mt-1">
        Gate viewer connections with a JWT signed by your keys, or a webhook your backend controls.
        Foghorn enforces on every viewer connect.
      </p>
    </div>

    <div class="flex items-center gap-2">
      {#if playbackPolicy}
        <Badge variant="outline" class="text-xs">
          Current: {playbackPolicy.type}
        </Badge>
      {/if}
    </div>
  </div>

  <div>
    <span class="block text-sm font-medium text-foreground mb-2">Policy type</span>
    <div class="grid grid-cols-1 sm:grid-cols-3 gap-2">
      <button
        type="button"
        class="border rounded-md p-3 text-left transition-colors hover:bg-muted/30 cursor-pointer"
        class:border-primary={policyType === "PUBLIC"}
        class:border-border={policyType !== "PUBLIC"}
        onclick={() => (policyType = "PUBLIC")}
      >
        <div class="flex items-center gap-2 mb-1">
          <Globe class="w-4 h-4 text-muted-foreground" />
          <span class="text-sm font-medium">Public</span>
        </div>
        <p class="text-xs text-muted-foreground">No auth. Anyone with the playback URL can view.</p>
      </button>

      <button
        type="button"
        class="border rounded-md p-3 text-left transition-colors hover:bg-muted/30 cursor-pointer"
        class:border-primary={policyType === "JWT"}
        class:border-border={policyType !== "JWT"}
        onclick={() => (policyType = "JWT")}
      >
        <div class="flex items-center gap-2 mb-1">
          <KeyIcon class="w-4 h-4 text-muted-foreground" />
          <span class="text-sm font-medium">JWT</span>
        </div>
        <p class="text-xs text-muted-foreground">
          Viewer presents a JWT signed by one of your keys.
        </p>
      </button>

      <button
        type="button"
        class="border rounded-md p-3 text-left transition-colors hover:bg-muted/30 cursor-pointer"
        class:border-primary={policyType === "WEBHOOK"}
        class:border-border={policyType !== "WEBHOOK"}
        onclick={() => (policyType = "WEBHOOK")}
      >
        <div class="flex items-center gap-2 mb-1">
          <Webhook class="w-4 h-4 text-muted-foreground" />
          <span class="text-sm font-medium">Webhook</span>
        </div>
        <p class="text-xs text-muted-foreground">
          FrameWorks POSTs to your URL on each viewer connect.
        </p>
      </button>
    </div>
  </div>

  {#if policyType === "JWT"}
    <div class="space-y-4">
      <div>
        <span class="block text-sm font-medium text-foreground mb-2">Allowed signing keys</span>
        {#if signingKeys.length === 0}
          <Alert variant="warning">
            <AlertDescription>
              No active signing keys.
              <a
                href={resolve("/developer/signing-keys")}
                class="text-primary hover:underline font-medium"
              >
                Create one in /developer/signing-keys
              </a>
              before saving a JWT policy.
            </AlertDescription>
          </Alert>
        {:else}
          <div class="border border-border rounded-md divide-y divide-border/50">
            {#each signingKeys as key (key.id)}
              <label class="flex items-center gap-3 p-3 hover:bg-muted/20 cursor-pointer">
                <input
                  type="checkbox"
                  checked={allowedKids.includes(key.kid)}
                  onchange={() => toggleKid(key.kid)}
                />
                <div class="flex-1 min-w-0">
                  <div class="text-sm font-medium text-foreground">{key.name}</div>
                  <div class="text-xs font-mono text-muted-foreground">{key.kid}</div>
                </div>
                <Badge variant="outline" class="text-xs">{key.algorithm}</Badge>
              </label>
            {/each}
          </div>
          <p class="text-xs text-muted-foreground mt-2">
            Empty selection = any active tenant key is accepted.
            <a href={resolve("/developer/signing-keys")} class="text-primary hover:underline"
              >Manage keys</a
            >.
          </p>
        {/if}
      </div>

      <div>
        <div class="flex items-center justify-between mb-2">
          <span class="block text-sm font-medium text-foreground">Required audience</span>
          <Button variant="outline" size="sm" class="h-7 gap-1" onclick={addAudience}>
            <Plus class="w-3 h-3" />
            Add
          </Button>
        </div>
        {#if requiredAudience.length === 0}
          <p class="text-xs text-muted-foreground">
            None. Viewer JWT's <code>aud</code> claim is not checked.
          </p>
        {:else}
          <div class="space-y-2">
            {#each requiredAudience as _, i (i)}
              <div class="flex items-center gap-2">
                <Input
                  type="text"
                  bind:value={requiredAudience[i]}
                  placeholder="e.g., my-app-prod"
                  class="flex-1"
                />
                <Button
                  variant="outline"
                  size="sm"
                  class="h-9 w-9 p-0"
                  onclick={() => removeAudience(i)}
                >
                  <Trash2 class="w-3.5 h-3.5" />
                </Button>
              </div>
            {/each}
          </div>
          <p class="text-xs text-muted-foreground mt-1">
            Viewer JWT's <code>aud</code> claim must contain at least one of these.
          </p>
        {/if}
      </div>

      <div>
        <div class="flex items-center justify-between mb-2">
          <span class="block text-sm font-medium text-foreground">Required claims</span>
          <Button variant="outline" size="sm" class="h-7 gap-1" onclick={addClaim}>
            <Plus class="w-3 h-3" />
            Add
          </Button>
        </div>
        {#if requiredClaims.length === 0}
          <p class="text-xs text-muted-foreground">No claim constraints.</p>
        {:else}
          <div class="space-y-2">
            {#each requiredClaims as _, i (i)}
              <div class="flex items-center gap-2">
                <Input
                  type="text"
                  bind:value={requiredClaims[i].name}
                  placeholder="claim name (e.g., tier)"
                  class="flex-1"
                />
                <Input
                  type="text"
                  bind:value={requiredClaims[i].jsonValue}
                  placeholder="JSON value (e.g., &quot;pro&quot; or 42)"
                  class="flex-1 font-mono text-xs"
                />
                <Button
                  variant="outline"
                  size="sm"
                  class="h-9 w-9 p-0"
                  onclick={() => removeClaim(i)}
                >
                  <Trash2 class="w-3.5 h-3.5" />
                </Button>
              </div>
            {/each}
          </div>
          <p class="text-xs text-muted-foreground mt-1">
            JSON-encoded values: strings as <code>"foo"</code>, numbers as <code>42</code>, arrays
            as
            <code>["a","b"]</code>.
          </p>
        {/if}
      </div>
    </div>
  {:else if policyType === "WEBHOOK"}
    <div class="space-y-4">
      <div>
        <label for="webhook-url" class="block text-sm font-medium text-foreground mb-2">
          Webhook URL *
        </label>
        <Input
          id="webhook-url"
          type="url"
          bind:value={webhookUrl}
          placeholder="https://your-backend.example.com/playback-auth"
          class="w-full"
        />
        <p class="text-xs text-muted-foreground mt-1">
          HTTPS only. FrameWorks POSTs viewer-connect details and reads allow/deny from your
          response.
        </p>
      </div>

      <div>
        <label for="webhook-secret" class="block text-sm font-medium text-foreground mb-2">
          {webhookHasExistingSecret ? "Replace HMAC secret" : "HMAC secret *"}
        </label>
        <Input
          id="webhook-secret"
          type="password"
          bind:value={webhookSecret}
          placeholder={webhookHasExistingSecret
            ? "leave empty to keep current secret"
            : "shared secret"}
          class="w-full font-mono"
        />
        <p class="text-xs text-muted-foreground mt-1">
          {webhookHasExistingSecret
            ? "Leave empty to keep the encrypted secret already configured."
            : "Used to sign the outbound POST body with HMAC-SHA256. Stored encrypted."}
        </p>
      </div>

      <div>
        <label for="webhook-timeout" class="block text-sm font-medium text-foreground mb-2">
          Timeout (ms)
        </label>
        <Select bind:value={webhookTimeoutMs} type="single">
          <SelectTrigger id="webhook-timeout" class="w-full">
            {webhookTimeoutMs} ms
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="1000">1000 ms</SelectItem>
            <SelectItem value="3000">3000 ms</SelectItem>
            <SelectItem value="5000">5000 ms (default)</SelectItem>
            <SelectItem value="10000">10000 ms (max)</SelectItem>
          </SelectContent>
        </Select>
      </div>
    </div>
  {:else}
    <Alert variant="info">
      <AlertDescription>
        Public playback. No JWT or webhook check; anyone with the playback URL can view. Switch to
        JWT or Webhook to gate viewer connections.
      </AlertDescription>
    </Alert>
  {/if}

  <div class="flex justify-end gap-2 pt-4 border-t border-border/50">
    <Button onclick={savePolicy} disabled={saving}>
      {saving ? "Saving..." : "Save policy"}
    </Button>
  </div>

  {#if playbackId}
    <PlaybackAccessTestPanel {playbackId} {policyType} />
  {/if}
</div>
