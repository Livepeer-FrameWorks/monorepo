<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { resolve } from "$app/paths";
  import { auth } from "$lib/stores/auth";
  import {
    GetSigningKeysConnectionStore,
    CreateSigningKeyStore,
    RevokeSigningKeyStore,
  } from "$houdini";
  import { toast } from "$lib/stores/toast.js";
  import SkeletonLoader from "$lib/components/SkeletonLoader.svelte";
  import { ShieldCheck, LogIn, Copy, Plus } from "lucide-svelte";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Badge } from "$lib/components/ui/badge";
  import { Alert, AlertDescription } from "$lib/components/ui/alert";
  import DeveloperCredentialsTabs from "$lib/components/developer/DeveloperCredentialsTabs.svelte";

  const keysStore = new GetSigningKeysConnectionStore();
  const createKeyMutation = new CreateSigningKeyStore();
  const revokeKeyMutation = new RevokeSigningKeyStore();

  let loadingMore = $state(false);
  let isAuthenticated = $state(false);

  let showCreateModal = $state(false);
  let newKeyName = $state("");
  let creating = $state(false);

  interface NewKeyDisplay {
    name: string;
    kid: string;
    privateKeyPem: string;
    publicKeyPem: string;
  }

  let newlyCreatedKey = $state<NewKeyDisplay | null>(null);

  let loading = $derived($keysStore.fetching);
  let signingKeys = $derived(
    $keysStore.data?.signingKeysConnection?.edges?.map((e) => e.node) ?? []
  );
  let hasMoreKeys = $derived(
    $keysStore.data?.signingKeysConnection?.pageInfo?.hasNextPage ?? false
  );
  let totalKeyCount = $derived($keysStore.data?.signingKeysConnection?.totalCount ?? 0);
  let activeKeyCount = $derived(signingKeys.filter((k) => k.status === "ACTIVE").length);

  const unsubscribeAuth = auth.subscribe((authState) => {
    isAuthenticated = authState.isAuthenticated;
  });

  onDestroy(() => {
    unsubscribeAuth();
  });

  onMount(async () => {
    if (!isAuthenticated) {
      await auth.checkAuth();
    }

    if (isAuthenticated) {
      await keysStore.fetch();
    }
  });

  async function createSigningKey() {
    if (!newKeyName.trim()) {
      toast.warning("Please enter a key name");
      return;
    }

    try {
      creating = true;
      const result = await createKeyMutation.mutate({
        input: {
          name: newKeyName.trim(),
        },
      });

      const data = result.data?.createSigningKey;
      if (data && data.__typename === "CreateSigningKeySuccess") {
        newlyCreatedKey = {
          name: data.signingKey.name,
          kid: data.signingKey.kid,
          privateKeyPem: data.privateKeyPem,
          publicKeyPem: data.signingKey.publicKeyPem,
        };
        await keysStore.fetch({ policy: "NetworkOnly" });
        newKeyName = "";
      } else if (data) {
        const errorResult = data as { message?: string };
        toast.error(errorResult.message || "Failed to create signing key");
      }
    } catch (error) {
      console.error("Failed to create signing key:", error);
      toast.error("Failed to create signing key. Please try again.");
    } finally {
      creating = false;
    }
  }

  async function revokeSigningKey(id: string, name: string) {
    if (
      !confirm(
        `Revoke signing key "${name}"? Any viewer JWTs signed with it will stop validating immediately.`
      )
    ) {
      return;
    }

    try {
      const result = await revokeKeyMutation.mutate({ id });
      const data = result.data?.revokeSigningKey;
      if (data?.__typename === "SigningKey") {
        await keysStore.fetch({ policy: "NetworkOnly" });
        toast.success("Signing key revoked");
      } else if (data) {
        const errorResult = data as { message?: string };
        toast.error(errorResult.message || "Failed to revoke signing key");
      } else {
        toast.error("Failed to revoke signing key");
      }
    } catch (error) {
      console.error("Failed to revoke signing key:", error);
      toast.error("Failed to revoke signing key. Please try again.");
    }
  }

  function copyToClipboard(text: string) {
    navigator.clipboard.writeText(text);
  }

  async function loadMoreKeys() {
    if (!hasMoreKeys || loadingMore) return;

    loadingMore = true;
    try {
      await keysStore.loadNextPage();
    } catch (err) {
      console.error("Failed to load more signing keys:", err);
      toast.error("Failed to load more signing keys");
    } finally {
      loadingMore = false;
    }
  }

  function formatDate(dateString: string | Date | null | undefined) {
    if (!dateString) return "—";
    return new Date(dateString).toLocaleDateString();
  }

  function getStatusBadgeClass(status: string) {
    switch (status.toUpperCase()) {
      case "ACTIVE":
        return "border-success/40 bg-success/10 text-success";
      case "REVOKED":
        return "border-destructive/40 bg-destructive/10 text-destructive";
      default:
        return "border-muted-foreground/40 bg-muted-foreground/10 text-muted-foreground";
    }
  }
</script>

<svelte:head>
  <title>Signing Keys - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col">
  <div class="px-4 sm:px-6 lg:px-8 py-3 border-b border-border shrink-0">
    <div class="flex justify-between items-center gap-4">
      <div class="flex items-center gap-3">
        <ShieldCheck class="w-5 h-5 text-primary" />
        <h1 class="text-lg font-bold text-foreground">Playback Signing Keys</h1>
        {#if totalKeyCount > 0}
          <span class="text-xs text-muted-foreground"
            >({signingKeys.length}{#if hasMoreKeys}+{/if})</span
          >
        {/if}
      </div>

      <div class="flex items-center gap-2">
        {#if isAuthenticated}
          <Button
            size="sm"
            class="gap-1.5"
            disabled={activeKeyCount >= 10}
            onclick={() => {
              showCreateModal = true;
              newlyCreatedKey = null;
            }}
          >
            <Plus class="w-3.5 h-3.5" />
            New Key
          </Button>
        {:else}
          <Button href={resolve("/login")} size="sm" class="gap-2">
            <LogIn class="w-4 h-4" />
            Login
          </Button>
        {/if}
      </div>
    </div>
  </div>
  <DeveloperCredentialsTabs active="signing-keys" />

  {#if loading}
    <div class="flex-1 flex items-center justify-center">
      <div class="text-center">
        <SkeletonLoader type="custom" class="w-8 h-8 rounded mx-auto mb-4" />
        <SkeletonLoader type="text" class="w-32 mx-auto" />
      </div>
    </div>
  {:else if !isAuthenticated}
    <div class="flex-1 flex items-center justify-center">
      <div class="text-center">
        <LogIn class="w-12 h-12 text-primary mx-auto mb-4" />
        <h2 class="text-xl font-semibold text-foreground mb-2">Authentication Required</h2>
        <p class="text-muted-foreground mb-6">Sign in to manage playback signing keys.</p>
        <Button href={resolve("/login")}>Sign In</Button>
      </div>
    </div>
  {:else}
    <div class="flex-1 overflow-y-auto">
      <div class="px-4 sm:px-6 lg:px-8 py-3">
        <p class="text-sm text-muted-foreground">
          Customer-managed ES256 keys for minting viewer playback JWTs. The private key is shown
          once at creation; FrameWorks stores only the public key. Up to 10 active keys per tenant.
          Apply a JWT policy on a stream's <strong>Playback Auth</strong> tab.
        </p>
      </div>

      {#if signingKeys.length === 0}
        <div class="px-4 py-12 text-center">
          <ShieldCheck class="w-10 h-10 text-muted-foreground mx-auto mb-3" />
          <p class="text-sm text-muted-foreground mb-4">
            No signing keys yet. Create one to start gating playback with JWTs.
          </p>
          <Button
            size="sm"
            class="gap-1.5"
            onclick={() => {
              showCreateModal = true;
              newlyCreatedKey = null;
            }}
          >
            <Plus class="w-3.5 h-3.5" />
            Create your first signing key
          </Button>
        </div>
      {:else}
        <table class="w-full text-sm">
          <thead class="bg-muted/30 sticky top-0">
            <tr class="text-left text-xs text-muted-foreground">
              <th class="px-4 py-2 font-medium">Name</th>
              <th class="px-4 py-2 font-medium">KID</th>
              <th class="px-4 py-2 font-medium">Algorithm</th>
              <th class="px-4 py-2 font-medium">Status</th>
              <th class="px-4 py-2 font-medium hidden sm:table-cell">Created</th>
              <th class="px-4 py-2 font-medium hidden md:table-cell">Last Used</th>
              <th class="px-4 py-2 font-medium hidden lg:table-cell">Revoked</th>
              <th class="px-4 py-2 font-medium text-right">Actions</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-border/50">
            {#each signingKeys as key, index (`${key.id}-${index}`)}
              <tr class="hover:bg-muted/20">
                <td class="px-4 py-2 font-medium text-foreground">{key.name}</td>
                <td class="px-4 py-2 font-mono text-xs text-muted-foreground">{key.kid}</td>
                <td class="px-4 py-2 text-muted-foreground">{key.algorithm}</td>
                <td class="px-4 py-2">
                  <Badge variant="outline" class={`text-xs ${getStatusBadgeClass(key.status)}`}>
                    {key.status}
                  </Badge>
                </td>
                <td class="px-4 py-2 text-muted-foreground hidden sm:table-cell"
                  >{formatDate(key.createdAt)}</td
                >
                <td class="px-4 py-2 text-muted-foreground hidden md:table-cell"
                  >{formatDate(key.lastUsedAt)}</td
                >
                <td class="px-4 py-2 text-muted-foreground hidden lg:table-cell"
                  >{formatDate(key.revokedAt)}</td
                >
                <td class="px-4 py-2 text-right">
                  {#if key.status === "ACTIVE"}
                    <button
                      class="text-xs text-destructive hover:underline cursor-pointer"
                      onclick={() => revokeSigningKey(key.id, key.name)}
                    >
                      Revoke
                    </button>
                  {:else}
                    <span class="text-xs text-muted-foreground">—</span>
                  {/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
        {#if hasMoreKeys}
          <div class="px-4 py-2 border-t border-border/50 text-center">
            <button
              class="text-xs text-primary hover:underline"
              onclick={loadMoreKeys}
              disabled={loadingMore}
            >
              {loadingMore ? "Loading..." : "Load more keys"}
            </button>
          </div>
        {/if}
      {/if}
    </div>
  {/if}
</div>

{#if showCreateModal}
  <div
    class="fixed inset-0 bg-black/50 backdrop-blur-sm flex items-center justify-center z-50"
    onclick={(e) => {
      if (e.target === e.currentTarget && !newlyCreatedKey) {
        showCreateModal = false;
      }
    }}
    onkeydown={(e) => {
      if (e.key === "Escape" && !newlyCreatedKey) {
        showCreateModal = false;
      }
    }}
    role="dialog"
    tabindex="0"
    aria-modal="true"
  >
    <div class="bg-card p-6 border border-border max-w-2xl w-full mx-4 rounded-lg shadow-xl">
      {#if newlyCreatedKey}
        <h3 class="text-xl font-semibold text-success mb-4 flex items-center gap-2">
          <ShieldCheck class="w-6 h-6" />
          Signing Key Created
        </h3>

        <div class="space-y-4">
          <div class="grid grid-cols-2 gap-4">
            <div>
              <label
                for="key-name-display"
                class="block text-sm font-medium text-muted-foreground mb-1"
              >
                Name
              </label>
              <p id="key-name-display" class="text-foreground font-semibold">
                {newlyCreatedKey.name}
              </p>
            </div>
            <div>
              <label
                for="key-kid-display"
                class="block text-sm font-medium text-muted-foreground mb-1"
              >
                KID (use in JWT header)
              </label>
              <p id="key-kid-display" class="text-foreground font-mono text-sm">
                {newlyCreatedKey.kid}
              </p>
            </div>
          </div>

          <div>
            <label
              for="private-key-display"
              class="block text-sm font-medium text-muted-foreground mb-2"
            >
              Private Key (Copy this now — it won't be shown again!)
            </label>
            <div class="flex space-x-2">
              <textarea
                id="private-key-display"
                value={newlyCreatedKey.privateKeyPem}
                readonly
                rows="8"
                class="flex-1 font-mono text-xs bg-muted p-2 rounded border border-border"
              ></textarea>
              <Button
                class="gap-2 self-start"
                onclick={() => copyToClipboard(newlyCreatedKey!.privateKeyPem)}
              >
                <Copy class="w-4 h-4" />
                Copy
              </Button>
            </div>
          </div>

          <div>
            <label
              for="public-key-display"
              class="block text-sm font-medium text-muted-foreground mb-2"
            >
              Public Key (FrameWorks already stored this; shown for reference)
            </label>
            <div class="flex space-x-2">
              <textarea
                id="public-key-display"
                value={newlyCreatedKey.publicKeyPem}
                readonly
                rows="4"
                class="flex-1 font-mono text-xs bg-muted p-2 rounded border border-border"
              ></textarea>
              <Button
                variant="outline"
                class="gap-2 self-start"
                onclick={() => copyToClipboard(newlyCreatedKey!.publicKeyPem)}
              >
                <Copy class="w-4 h-4" />
                Copy
              </Button>
            </div>
          </div>

          <Alert variant="warning">
            <AlertDescription>
              <strong>Store the private key securely.</strong> FrameWorks does not retain it. Lose it
              and you'll need to revoke and create a new key.
            </AlertDescription>
          </Alert>
        </div>

        <div class="flex justify-end space-x-3 mt-6">
          <Button
            onclick={() => {
              showCreateModal = false;
              newlyCreatedKey = null;
            }}
          >
            I've Saved the Private Key
          </Button>
        </div>
      {:else}
        <h3 class="text-xl font-semibold text-foreground mb-4">Create New Signing Key</h3>

        <div class="space-y-4">
          <div>
            <label for="key-name" class="block text-sm font-medium text-muted-foreground mb-2">
              Name *
            </label>
            <Input
              id="key-name"
              type="text"
              bind:value={newKeyName}
              placeholder="e.g., production, staging, rotation-2026-q2"
              class="w-full"
              disabled={creating}
            />
          </div>

          <Alert variant="info">
            <AlertDescription>
              <p>
                FrameWorks generates an ES256 keypair, returns the private key once, and stores only
                the public key. Use the private key in your backend to mint viewer JWTs; reference
                the returned <code>kid</code> in the JWT header.
              </p>
            </AlertDescription>
          </Alert>
        </div>

        <div class="flex justify-end space-x-3 mt-6">
          <Button
            variant="outline"
            onclick={() => {
              showCreateModal = false;
              newKeyName = "";
            }}
            disabled={creating}
          >
            Cancel
          </Button>
          <Button onclick={createSigningKey} disabled={creating || !newKeyName.trim()}>
            {creating ? "Creating..." : "Create Signing Key"}
          </Button>
        </div>
      {/if}
    </div>
  </div>
{/if}
