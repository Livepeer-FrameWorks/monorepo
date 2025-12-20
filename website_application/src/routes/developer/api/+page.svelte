<script lang="ts">
  import { onMount } from "svelte";
  import { resolve } from "$app/paths";
  import { auth } from "$lib/stores/auth";
  import { GetAPITokensConnectionStore, CreateAPITokenStore, RevokeAPITokenStore } from "$houdini";
  import { toast } from "$lib/stores/toast.js";
  import SkeletonLoader from "$lib/components/SkeletonLoader.svelte";
  import GraphQLExplorer from "$lib/components/GraphQLExplorer.svelte";
  import {
    Code2,
    Key,
    LogIn,
    Copy,
    Plus,
    ChevronDown,
    ChevronUp,
  } from "lucide-svelte";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Badge } from "$lib/components/ui/badge";
  import {
    Select,
    SelectTrigger,
    SelectContent,
    SelectItem,
  } from "$lib/components/ui/select";
  import { Alert, AlertDescription } from "$lib/components/ui/alert";

  // Houdini stores - names must match the query/mutation names in .gql files
  const tokensStore = new GetAPITokensConnectionStore();
  const createTokenMutation = new CreateAPITokenStore();
  const revokeTokenMutation = new RevokeAPITokenStore();

  // Pagination state
  let loadingMore = $state(false);

  let isAuthenticated = $state(false);
  // Placeholder for code examples - users should use their Developer API Token
  let authToken = $state<string | null>("YOUR_API_TOKEN");

  // API Token Management
  let showCreateTokenModal = $state(false);
  let newTokenName = $state("");
  let newTokenExpiry = $state("0"); // "0" = never expires
  const tokenExpiryLabels: Record<string, string> = {
    "0": "Never expires",
    "30": "30 days",
    "90": "90 days",
    "365": "1 year",
  };

  interface NewTokenDisplay {
    token_name: string;
    token_value: string;
  }

  let newlyCreatedToken = $state<NewTokenDisplay | null>(null);
  let creatingToken = $state(false);
  let tokensExpanded = $state(true);

  // Derived state from Houdini stores
  let loading = $derived($tokensStore.fetching);
  let apiTokens = $derived(
    $tokensStore.data?.developerTokensConnection?.edges?.map(e => e.node) ?? []
  );
  let hasMoreTokens = $derived(
    $tokensStore.data?.developerTokensConnection?.pageInfo?.hasNextPage ?? false
  );
  let totalTokenCount = $derived(
    $tokensStore.data?.developerTokensConnection?.totalCount ?? 0
  );

  // Subscribe to auth store
  auth.subscribe((authState) => {
    isAuthenticated = authState.isAuthenticated;
  });

  onMount(async () => {
    if (!isAuthenticated) {
      await auth.checkAuth();
    }

    if (isAuthenticated) {
      await tokensStore.fetch();
    }
  });

  async function createAPIToken() {
    if (!newTokenName.trim()) {
      toast.warning("Please enter a token name");
      return;
    }

    try {
      creatingToken = true;
      const result = await createTokenMutation.mutate({
        input: {
          name: newTokenName.trim(),
          permissions: "read,write",
          expiresIn: Number(newTokenExpiry) || null,
        },
      });

      const data = result.data?.createDeveloperToken;
      if (data && data.__typename === "DeveloperToken") {
        newlyCreatedToken = {
          token_name: data.tokenName,
          token_value: data.tokenValue || "", // Should be present on creation
        };
        // Houdini's @list directive with @prepend automatically updates the cache
        // No manual refetch needed - that causes duplicate entries

        // Reset form but keep modal open to show the token
        newTokenName = "";
        newTokenExpiry = "0";
      } else if (data) {
        // Handle error types
        const errorResult = data as { message?: string };
        toast.error(errorResult.message || "Failed to create token");
      }
    } catch (error) {
      console.error("Failed to create API token:", error);
      toast.error("Failed to create API token. Please try again.");
    } finally {
      creatingToken = false;
    }
  }

  async function revokeAPIToken(tokenId: string, tokenName: string) {
    if (
      !confirm(
        `Are you sure you want to revoke the token "${tokenName}"? This action cannot be undone.`,
      )
    ) {
      return;
    }

    try {
      await revokeTokenMutation.mutate({ id: tokenId });
      // Refetch to update the list
      await tokensStore.fetch({ policy: "NetworkOnly" });
      toast.success("API token revoked successfully");
    } catch (error) {
      console.error("Failed to revoke API token:", error);
      toast.error("Failed to revoke API token. Please try again.");
    }
  }

  function copyToClipboard(text: string) {
    navigator.clipboard.writeText(text);
  }

  async function loadMoreTokens() {
    if (!hasMoreTokens || loadingMore) return;

    loadingMore = true;
    try {
      await tokensStore.loadNextPage();
    } catch (err) {
      console.error("Failed to load more tokens:", err);
      toast.error("Failed to load more tokens");
    } finally {
      loadingMore = false;
    }
  }

  function formatDate(dateString: string | Date | null | undefined) {
    if (!dateString) return "—";
    return new Date(dateString).toLocaleDateString();
  }

  function getStatusBadgeClass(status: string) {
    switch (status.toLowerCase()) {
      case "active":
        return "border-success/40 bg-success/10 text-success";
      case "revoked":
        return "border-destructive/40 bg-destructive/10 text-destructive";
      case "expired":
        return "border-warning/40 bg-warning/10 text-warning";
      default:
        return "border-muted-foreground/40 bg-muted-foreground/10 text-muted-foreground";
    }
  }

</script>

<svelte:head>
  <title>GraphQL API - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col">
    <!-- Compact Page Header -->
    <div class="px-4 sm:px-6 lg:px-8 py-3 border-b border-border shrink-0">
      <div class="flex justify-between items-center gap-4">
        <div class="flex items-center gap-3">
          <Code2 class="w-5 h-5 text-primary" />
          <h1 class="text-lg font-bold text-foreground">GraphQL API</h1>
        </div>

        <div class="flex items-center gap-4 text-xs">
          <div class="hidden md:flex items-center gap-4">
            <div class="flex items-center gap-1.5">
              <span class="text-muted-foreground">HTTP</span>
              <code class="font-mono text-foreground bg-muted px-1.5 py-0.5">{(import.meta as any).env.VITE_GRAPHQL_HTTP_URL || "http://localhost:18000/graphql/"}</code>
            </div>
            <div class="flex items-center gap-1.5">
              <span class="text-muted-foreground">WS</span>
              <code class="font-mono text-foreground bg-muted px-1.5 py-0.5">{(import.meta as any).env.VITE_GRAPHQL_WS_URL || "ws://localhost:18000/graphql/"}</code>
            </div>
          </div>
          {#if !isAuthenticated}
            <Button href={resolve("/login")} size="sm" class="gap-2">
              <LogIn class="w-4 h-4" />
              Login
            </Button>
          {/if}
        </div>
      </div>
    </div>

    {#if loading}
      <!-- Loading Skeleton -->
      <div class="flex-1 flex items-center justify-center">
        <div class="text-center">
          <SkeletonLoader type="custom" class="w-8 h-8 rounded mx-auto mb-4" />
          <SkeletonLoader type="text" class="w-32 mx-auto" />
        </div>
      </div>
    {:else if !isAuthenticated}
      <!-- Not Authenticated State -->
      <div class="flex-1 flex items-center justify-center">
        <div class="text-center">
          <LogIn class="w-12 h-12 text-primary mx-auto mb-4" />
          <h2 class="text-xl font-semibold text-foreground mb-2">Authentication Required</h2>
          <p class="text-muted-foreground mb-6">
            Sign in to access the GraphQL API explorer and manage your API tokens.
          </p>
          <Button href={resolve("/login")}>Sign In</Button>
        </div>
      </div>
    {:else}
      <!-- Main Content: Stacked Layout -->
      <div class="flex-1 flex flex-col overflow-hidden">
        <!-- API Tokens Panel (collapsible) -->
        <div class="border-b border-border shrink-0">
          <!-- Header -->
          <button
            class="w-full px-4 py-2 flex items-center justify-between hover:bg-muted/30 transition-colors"
            onclick={() => tokensExpanded = !tokensExpanded}
          >
            <div class="flex items-center gap-2">
              <Key class="w-4 h-4 text-muted-foreground" />
              <span class="font-medium text-sm text-foreground">API Tokens</span>
              {#if totalTokenCount > 0}
                <span class="text-xs text-muted-foreground">({apiTokens.length}{#if hasMoreTokens}+{/if})</span>
              {/if}
            </div>
            <div class="flex items-center gap-2">
              <Button
                variant="outline"
                size="sm"
                class="gap-1 h-6 text-xs"
                onclick={(e: MouseEvent) => {
                  e.stopPropagation();
                  showCreateTokenModal = true;
                  newlyCreatedToken = null;
                }}
              >
                <Plus class="w-3 h-3" />
                New Token
              </Button>
              {#if tokensExpanded}
                <ChevronUp class="w-4 h-4 text-muted-foreground" />
              {:else}
                <ChevronDown class="w-4 h-4 text-muted-foreground" />
              {/if}
            </div>
          </button>

          <!-- Tokens Table (collapsible) -->
          {#if tokensExpanded}
            <div class="max-h-48 overflow-y-auto">
              {#if apiTokens.length === 0}
                <div class="px-4 py-4 text-center text-sm text-muted-foreground">
                  No API tokens yet. Create one to get started.
                </div>
              {:else}
                <table class="w-full text-sm">
                  <thead class="bg-muted/30 sticky top-0">
                    <tr class="text-left text-xs text-muted-foreground">
                      <th class="px-4 py-2 font-medium">Name</th>
                      <th class="px-4 py-2 font-medium">Status</th>
                      <th class="px-4 py-2 font-medium hidden sm:table-cell">Created</th>
                      <th class="px-4 py-2 font-medium hidden md:table-cell">Last Used</th>
                      <th class="px-4 py-2 font-medium hidden lg:table-cell">Expires</th>
                      <th class="px-4 py-2 font-medium text-right">Actions</th>
                    </tr>
                  </thead>
                  <tbody class="divide-y divide-border/50">
                    {#each apiTokens as token, index (`${token.id}-${index}`)}
                      <tr class="hover:bg-muted/20">
                        <td class="px-4 py-2 font-medium text-foreground">{token.tokenName}</td>
                        <td class="px-4 py-2">
                          <Badge variant="outline" class={`text-xs ${getStatusBadgeClass(token.status)}`}>
                            {token.status}
                          </Badge>
                        </td>
                        <td class="px-4 py-2 text-muted-foreground hidden sm:table-cell">{formatDate(token.createdAt)}</td>
                        <td class="px-4 py-2 text-muted-foreground hidden md:table-cell">{formatDate(token.lastUsedAt)}</td>
                        <td class="px-4 py-2 text-muted-foreground hidden lg:table-cell">{formatDate(token.expiresAt)}</td>
                        <td class="px-4 py-2 text-right">
                          {#if token.status.toLowerCase() === "active"}
                            <button
                              class="text-xs text-destructive hover:underline cursor-pointer"
                              onclick={() => revokeAPIToken(token.id, token.tokenName)}
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
                {#if hasMoreTokens}
                  <div class="px-4 py-2 border-t border-border/50 text-center">
                    <button
                      class="text-xs text-primary hover:underline"
                      onclick={loadMoreTokens}
                      disabled={loadingMore}
                    >
                      {loadingMore ? "Loading..." : "Load more tokens"}
                    </button>
                  </div>
                {/if}
              {/if}
            </div>
          {/if}
        </div>

        <!-- GraphQL Explorer (fills remaining space) -->
        <div class="flex-1 overflow-hidden">
          <GraphQLExplorer {authToken} />
        </div>
      </div>
    {/if}
</div>

<!-- Create Token Modal -->
{#if showCreateTokenModal}
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
  <div
    class="fixed inset-0 bg-black/50 backdrop-blur-sm flex items-center justify-center z-50"
    onclick={(e) => {
      // Close modal when clicking backdrop (not the modal content)
      if (e.target === e.currentTarget && !newlyCreatedToken) {
        showCreateTokenModal = false;
      }
    }}
    onkeydown={(e) => {
      if (e.key === "Escape" && !newlyCreatedToken) {
        showCreateTokenModal = false;
      }
    }}
    role="dialog"
    tabindex="0"
    aria-modal="true"
  >
    <div
      class="bg-card p-6 border border-border max-w-md w-full mx-4 rounded-lg shadow-xl"
    >
      {#if newlyCreatedToken}
        <!-- Show newly created token -->
        <h3
          class="text-xl font-semibold text-success mb-4 flex items-center gap-2"
        >
          <Key class="w-6 h-6" />
          Token Created Successfully!
        </h3>

        <div class="space-y-4">
          <div>
            <label
              for="token-name-display"
              class="block text-sm font-medium text-muted-foreground mb-2"
            >
              Token Name
            </label>
            <p
              id="token-name-display"
              class="text-foreground font-semibold"
            >
              {newlyCreatedToken.token_name}
            </p>
          </div>

          <div>
            <label
              for="api-token-display"
              class="block text-sm font-medium text-muted-foreground mb-2"
            >
              API Token (Copy this now - it won't be shown again!)
            </label>
            <div class="flex space-x-2">
              <Input
                id="api-token-display"
                type="text"
                value={newlyCreatedToken.token_value}
                readonly
                class="flex-1 font-mono text-sm bg-muted"
              />
              <Button
                class="gap-2"
                onclick={() => copyToClipboard(newlyCreatedToken!.token_value)}
              >
                <Copy class="w-4 h-4" />
                Copy
              </Button>
            </div>
          </div>

          <Alert variant="warning">
            <AlertDescription>
              <strong>Important:</strong> Store this token securely. You won't be
              able to see it again after closing this dialog.
            </AlertDescription>
          </Alert>
        </div>

        <div class="flex justify-end space-x-3 mt-6">
          <Button
            onclick={() => {
              showCreateTokenModal = false;
              newlyCreatedToken = null;
            }}
          >
            I've Saved the Token
          </Button>
        </div>
      {:else}
        <!-- Create token form -->
        <h3 class="text-xl font-semibold text-foreground mb-4">
          Create New API Token
        </h3>

        <div class="space-y-4">
          <div>
            <label
              for="token-name"
              class="block text-sm font-medium text-muted-foreground mb-2"
            >
              Token Name *
            </label>
            <Input
              id="token-name"
              type="text"
              bind:value={newTokenName}
              placeholder="e.g., My App Token, Production API, etc."
              class="w-full"
              disabled={creatingToken}
            />
          </div>

          <div>
            <label
              for="token-expiry"
              class="block text-sm font-medium text-muted-foreground mb-2"
            >
              Expires In
            </label>
            <Select bind:value={newTokenExpiry} type="single">
              <SelectTrigger
                id="token-expiry"
                class="w-full"
                disabled={creatingToken}
              >
                {tokenExpiryLabels[newTokenExpiry] ?? "Expiration"}
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="0">Never expires</SelectItem>
                <SelectItem value="30">30 days</SelectItem>
                <SelectItem value="90">90 days</SelectItem>
                <SelectItem value="365">1 year</SelectItem>
              </SelectContent>
            </Select>
          </div>

          <Alert variant="info">
            <AlertDescription>
              <strong>Tip:</strong> Create separate tokens for different applications
              or environments (development, staging, production).
            </AlertDescription>
          </Alert>
        </div>

        <div class="flex justify-end space-x-3 mt-6">
          <Button
            variant="outline"
            onclick={() => {
              showCreateTokenModal = false;
              newTokenName = "";
              newTokenExpiry = "0";
            }}
            disabled={creatingToken}
          >
            Cancel
          </Button>
          <Button
            onclick={createAPIToken}
            disabled={creatingToken || !newTokenName.trim()}
          >
            {creatingToken ? "Creating..." : "Create Token"}
          </Button>
        </div>
      {/if}
    </div>
  </div>
{/if}
