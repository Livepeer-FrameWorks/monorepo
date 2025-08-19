<script>
  import { onMount } from "svelte";
  import { base } from "$app/paths";
  import { auth } from "$lib/stores/auth";
  import { developerService } from "$lib/graphql/services/developer.js";
  import { toast } from "$lib/stores/toast.js";
  import SkeletonLoader from "$lib/components/SkeletonLoader.svelte";
  import GraphQLExplorer from "$lib/components/GraphQLExplorer.svelte";
  import { Code2, Key, LogIn, Globe, Zap, Target, Copy, Plus, Rocket, BookOpen } from 'lucide-svelte';

  let isAuthenticated = false;
  /** @type {any} */
  let user = null;
  let loading = true;
  let authToken = null;

  // API Token Management
  let apiTokens = [];
  let showCreateTokenModal = false;
  let creatingToken = false;
  let newTokenName = "";
  let newTokenExpiry = 0; // 0 = never expires
  let newlyCreatedToken = null;

  // Subscribe to auth store
  auth.subscribe((authState) => {
    isAuthenticated = authState.isAuthenticated;
    user = authState.user?.user || null;
    authToken = authState.token || null;
  });

  onMount(async () => {
    if (!isAuthenticated) {
      await auth.checkAuth();
    }
    loading = false;

    if (isAuthenticated) {
      await loadAPITokens();
    }
  });

  async function loadAPITokens() {
    try {
      apiTokens = await developerService.getAPITokens();
    } catch (error) {
      console.error("Failed to load API tokens:", error);
      toast.error("Failed to load API tokens. Please refresh the page.");
    }
  }

  async function createAPIToken() {
    if (!newTokenName.trim()) {
      toast.warning("Please enter a token name");
      return;
    }

    try {
      creatingToken = true;
      const result = await developerService.createAPIToken({
        name: newTokenName.trim(),
        permissions: "read,write",
        expiresIn: newTokenExpiry || null,
      });

      if (result) {
        newlyCreatedToken = {
          token_name: result.name,
          token_value: result.token
        };
        await loadAPITokens();

        // Reset form but keep modal open to show the token
        newTokenName = "";
        newTokenExpiry = 0;
      }
    } catch (error) {
      console.error("Failed to create API token:", error);
      toast.error("Failed to create API token. Please try again.");
    } finally {
      creatingToken = false;
    }
  }

  async function revokeAPIToken(tokenId, tokenName) {
    if (
      !confirm(
        `Are you sure you want to revoke the token "${tokenName}"? This action cannot be undone.`
      )
    ) {
      return;
    }

    try {
      await developerService.revokeAPIToken(tokenId);
      await loadAPITokens();
      toast.success("API token revoked successfully");
    } catch (error) {
      console.error("Failed to revoke API token:", error);
      toast.error("Failed to revoke API token. Please try again.");
    }
  }

  function copyToClipboard(text) {
    navigator.clipboard.writeText(text);
  }

  function formatDate(dateString) {
    if (!dateString) return "Never";
    return new Date(dateString).toLocaleString();
  }

  function getTokenStatusColor(status) {
    switch (status) {
      case "active":
        return "text-tokyo-night-green";
      case "expired":
        return "text-tokyo-night-yellow";
      case "revoked":
        return "text-tokyo-night-red";
      default:
        return "text-tokyo-night-comment";
    }
  }
</script>

<svelte:head>
  <title>GraphQL API - FrameWorks</title>
</svelte:head>

<div class="space-y-8 page-transition">
  <!-- Page Header -->
  <div class="flex justify-between items-start">
    <div>
      <h1 class="text-3xl font-bold text-tokyo-night-fg mb-2 flex items-center gap-3">
        <Code2 class="w-8 h-8" />
        GraphQL API
      </h1>
      <p class="text-tokyo-night-fg-dark">
        Interactive GraphQL explorer with schema introspection, query templates, and code generation
      </p>
    </div>

    <div class="flex space-x-3">
      {#if isAuthenticated}
        <button
          class="btn-primary flex items-center gap-2"
          on:click={() => {
            showCreateTokenModal = true;
            newlyCreatedToken = null;
          }}
        >
          <Key class="w-4 h-4" />
          Create New Token
        </button>
      {:else}
        <a href="{base}/login" class="btn-primary flex items-center gap-2">
          <LogIn class="w-4 h-4" />
          Login to Access
        </a>
      {/if}
    </div>
  </div>

  {#if loading}
    <!-- API Tokens Skeleton -->
    <div class="bg-tokyo-night-surface rounded-lg p-6 mb-8">
      <SkeletonLoader type="text-lg" className="w-32 mb-4" />
      <div class="space-y-3">
        {#each Array(3) as _}
          <div class="flex items-center justify-between p-4 bg-tokyo-night-bg rounded-lg border border-tokyo-night-selection">
            <div class="flex-1">
              <SkeletonLoader type="text" className="w-40 mb-2" />
              <SkeletonLoader type="text-sm" className="w-32" />
            </div>
            <div class="flex space-x-2">
              <SkeletonLoader type="custom" className="w-16 h-8 rounded" />
              <SkeletonLoader type="custom" className="w-8 h-8 rounded" />
            </div>
          </div>
        {/each}
      </div>
    </div>
  {:else if !isAuthenticated}
    <!-- Not Authenticated State -->
    <div class="card text-center py-12">
      <div class="flex justify-center mb-4">
        <LogIn class="w-16 h-16 text-tokyo-night-blue" />
      </div>
      <h3 class="text-xl font-semibold text-tokyo-night-fg mb-2">
        Authentication Required
      </h3>
      <p class="text-tokyo-night-fg-dark mb-6">
        Please sign in to access the GraphQL API explorer and manage your API keys.
      </p>
      <a href="{base}/login" class="btn-primary"> Sign In </a>
    </div>
  {:else}
    <!-- GraphQL API Overview -->
    <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-6">
      <div class="glow-card p-6">
        <div class="flex items-start justify-between">
          <div class="flex-1 min-w-0">
            <p class="text-sm text-tokyo-night-comment mb-2">GraphQL Endpoint</p>
            <p class="text-sm font-mono text-tokyo-night-fg break-all leading-relaxed">
              {import.meta.env.VITE_GRAPHQL_HTTP_URL || 'http://localhost:18000/graphql/'}
            </p>
          </div>
          <Globe class="w-6 h-6 ml-3 flex-shrink-0 text-tokyo-night-blue" />
        </div>
      </div>

      <div class="glow-card p-6">
        <div class="flex items-start justify-between">
          <div class="flex-1 min-w-0">
            <p class="text-sm text-tokyo-night-comment mb-2">WebSocket</p>
            <p class="text-sm font-mono text-tokyo-night-fg break-all leading-relaxed">
              {import.meta.env.VITE_GRAPHQL_WS_URL || 'ws://localhost:18000/graphql/'}
            </p>
          </div>
          <Zap class="w-6 h-6 ml-3 flex-shrink-0 text-tokyo-night-yellow" />
        </div>
      </div>

      <div class="glow-card p-6">
        <div class="flex items-start justify-between">
          <div class="flex-1 min-w-0">
            <p class="text-sm text-tokyo-night-comment mb-2">Authentication</p>
            <p class="text-lg font-semibold text-tokyo-night-fg">JWT Bearer</p>
          </div>
          <Key class="w-6 h-6 ml-3 flex-shrink-0 text-tokyo-night-green" />
        </div>
      </div>

      <div class="glow-card p-6">
        <div class="flex items-start justify-between">
          <div class="flex-1 min-w-0">
            <p class="text-sm text-tokyo-night-comment mb-2">Active Tokens</p>
            <p class="text-lg font-semibold text-tokyo-night-fg">
              {apiTokens.filter((t) => t.status === "active").length}
            </p>
          </div>
          <Target class="w-6 h-6 ml-3 flex-shrink-0 text-tokyo-night-purple" />
        </div>
      </div>
    </div>

    <!-- API Token Management -->
    <div class="card">
      <div class="card-header">
        <h2 class="text-xl font-semibold text-tokyo-night-fg mb-2 flex items-center gap-2">
          <Key class="w-5 h-5" />
          Your API Tokens
        </h2>
        <p class="text-tokyo-night-fg-dark">
          Generate and manage API tokens for programmatic access to your streams
        </p>
      </div>

      {#if apiTokens.length === 0}
        <div class="text-center py-8">
          <div class="flex justify-center mb-4">
            <Key class="w-12 h-12 text-tokyo-night-blue" />
          </div>
          <h3 class="text-lg font-semibold text-tokyo-night-fg mb-2">
            No API Tokens
          </h3>
          <p class="text-tokyo-night-comment mb-4">
            Create your first API token to start using the FrameWorks GraphQL API
          </p>
          <button
            class="btn-primary flex items-center gap-2"
            on:click={() => {
              showCreateTokenModal = true;
              newlyCreatedToken = null;
            }}
          >
            <Plus class="w-4 h-4" />
            Create Your First Token
          </button>
        </div>
      {:else}
        <div class="space-y-4">
          {#each apiTokens as token}
            <div
              class="bg-tokyo-night-bg-highlight p-4 rounded-lg border border-tokyo-night-fg-gutter"
            >
              <div class="flex items-center justify-between">
                <div class="flex-1">
                  <div class="flex items-center space-x-3 mb-2">
                    <h3 class="font-semibold text-tokyo-night-fg">
                      {token.name}
                    </h3>
                    <span
                      class="text-xs px-2 py-1 rounded {getTokenStatusColor(
                        token.status
                      )} bg-tokyo-night-bg"
                    >
                      {token.status.toUpperCase()}
                    </span>
                  </div>
                  <div class="grid grid-cols-2 gap-4 text-sm">
                    <div>
                      <p class="text-tokyo-night-comment">Permissions</p>
                      <p class="text-tokyo-night-fg">{token.permissions}</p>
                    </div>
                    <div>
                      <p class="text-tokyo-night-comment">Last Used</p>
                      <p class="text-tokyo-night-fg">
                        {formatDate(token.lastUsedAt)}
                      </p>
                    </div>
                    <div>
                      <p class="text-tokyo-night-comment">Expires</p>
                      <p class="text-tokyo-night-fg">
                        {token.expiresAt
                          ? formatDate(token.expiresAt)
                          : "Never"}
                      </p>
                    </div>
                    <div>
                      <p class="text-tokyo-night-comment">Created</p>
                      <p class="text-tokyo-night-fg">
                        {formatDate(token.createdAt)}
                      </p>
                    </div>
                  </div>
                </div>
                <div class="flex space-x-2">
                  {#if token.status === "active"}
                    <button
                      class="btn-danger text-sm px-3 py-1"
                      on:click={() =>
                        revokeAPIToken(token.id, token.name)}
                    >
                      Revoke
                    </button>
                  {/if}
                </div>
              </div>
            </div>
          {/each}
        </div>
      {/if}
    </div>

    <!-- GraphQL Explorer -->
    <div class="card">
      <div class="card-header">
        <h2 class="text-xl font-semibold text-tokyo-night-fg mb-2 flex items-center gap-2">
          <Rocket class="w-5 h-5" />
          GraphQL API Explorer
        </h2>
        <p class="text-tokyo-night-fg-dark">
          Interactive GraphQL query builder and tester with live schema introspection
        </p>
      </div>
      
      <GraphQLExplorer {authToken} />
    </div>

    <!-- GraphQL Guide -->
    <div class="card">
      <div class="card-header">
        <h2 class="text-xl font-semibold text-tokyo-night-fg mb-2 flex items-center gap-2">
          <BookOpen class="w-5 h-5" />
          GraphQL API Guide
        </h2>
        <p class="text-tokyo-night-fg-dark">
          Everything you need to know about our GraphQL API
        </p>
      </div>

      <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-6">
        <div class="text-center">
          <div class="flex justify-center mb-3">
            <Code2 class="w-8 h-8 text-tokyo-night-blue" />
          </div>
          <h3 class="font-semibold text-tokyo-night-fg mb-2">Schema First</h3>
          <p class="text-sm text-tokyo-night-comment">
            All operations use a single GraphQL endpoint. Query exactly the data you need with strong typing.
          </p>
        </div>

        <div class="text-center">
          <div class="flex justify-center mb-3">
            <Zap class="w-8 h-8 text-tokyo-night-yellow" />
          </div>
          <h3 class="font-semibold text-tokyo-night-fg mb-2">Real-time</h3>
          <p class="text-sm text-tokyo-night-comment">
            Use GraphQL subscriptions over WebSocket for real-time stream events and viewer metrics.
          </p>
        </div>

        <div class="text-center">
          <div class="flex justify-center mb-3">
            <Key class="w-8 h-8 text-tokyo-night-green" />
          </div>
          <h3 class="font-semibold text-tokyo-night-fg mb-2">JWT Auth</h3>
          <p class="text-sm text-tokyo-night-comment">
            Include your JWT token in the Authorization header. The explorer handles this automatically.
          </p>
        </div>

        <div class="text-center">
          <div class="flex justify-center mb-3">
            <Target class="w-8 h-8 text-tokyo-night-purple" />
          </div>
          <h3 class="font-semibold text-tokyo-night-fg mb-2">Type Safe</h3>
          <p class="text-sm text-tokyo-night-comment">
            Generate TypeScript types from the schema for full type safety in your applications.
          </p>
        </div>
      </div>
    </div>
  {/if}
</div>

<!-- Create Token Modal -->
{#if showCreateTokenModal}
  <div
    class="fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center z-50"
  >
    <div
      class="bg-tokyo-night-bg-light p-6 rounded-lg border border-tokyo-night-fg-gutter max-w-md w-full mx-4"
    >
      {#if newlyCreatedToken}
        <!-- Show newly created token -->
        <h3 class="text-xl font-semibold text-tokyo-night-green mb-4 flex items-center gap-2">
          <Key class="w-6 h-6" />
          Token Created Successfully!
        </h3>

        <div class="space-y-4">
          <div>
            <label
              for="token-name-display"
              class="block text-sm font-medium text-tokyo-night-fg-dark mb-2"
            >
              Token Name
            </label>
            <p id="token-name-display" class="text-tokyo-night-fg font-semibold">
              {newlyCreatedToken.token_name}
            </p>
          </div>

          <div>
            <label
              for="api-token-display"
              class="block text-sm font-medium text-tokyo-night-fg-dark mb-2"
            >
              API Token (Copy this now - it won't be shown again!)
            </label>
            <div class="flex space-x-2">
              <input
                id="api-token-display"
                type="text"
                value={newlyCreatedToken.token_value}
                readonly
                class="input flex-1 font-mono text-sm bg-tokyo-night-bg-highlight"
              />
              <button
                on:click={() => copyToClipboard(newlyCreatedToken.token_value)}
                class="btn-primary flex items-center gap-2"
              >
                <Copy class="w-4 h-4" />
                Copy
              </button>
            </div>
          </div>

          <div
            class="bg-tokyo-night-bg-highlight p-3 rounded border border-tokyo-night-yellow"
          >
            <p class="text-sm text-tokyo-night-yellow">
              <strong>Important:</strong> Store this token securely. You won't
              be able to see it again after closing this dialog.
            </p>
          </div>
        </div>

        <div class="flex justify-end space-x-3 mt-6">
          <button
            class="btn-primary"
            on:click={() => {
              showCreateTokenModal = false;
              newlyCreatedToken = null;
            }}
          >
            I've Saved the Token
          </button>
        </div>
      {:else}
        <!-- Create token form -->
        <h3 class="text-xl font-semibold text-tokyo-night-fg mb-4">
          Create New API Token
        </h3>

        <div class="space-y-4">
          <div>
            <label
              for="token-name"
              class="block text-sm font-medium text-tokyo-night-fg-dark mb-2"
            >
              Token Name *
            </label>
            <input
              id="token-name"
              type="text"
              bind:value={newTokenName}
              placeholder="e.g., My App Token, Production API, etc."
              class="input w-full"
              disabled={creatingToken}
            />
          </div>

          <div>
            <label
              for="token-expiry"
              class="block text-sm font-medium text-tokyo-night-fg-dark mb-2"
            >
              Expires In
            </label>
            <select
              id="token-expiry"
              bind:value={newTokenExpiry}
              class="input w-full"
              disabled={creatingToken}
            >
              <option value={0}>Never expires</option>
              <option value={30}>30 days</option>
              <option value={90}>90 days</option>
              <option value={365}>1 year</option>
            </select>
          </div>

          <div
            class="bg-tokyo-night-bg-highlight p-3 rounded border border-tokyo-night-fg-gutter"
          >
            <p class="text-sm text-tokyo-night-comment">
              <strong>Tip:</strong> Create separate tokens for different applications
              or environments (development, staging, production).
            </p>
          </div>
        </div>

        <div class="flex justify-end space-x-3 mt-6">
          <button
            class="btn-secondary"
            on:click={() => {
              showCreateTokenModal = false;
              newTokenName = "";
              newTokenExpiry = 0;
            }}
            disabled={creatingToken}
          >
            Cancel
          </button>
          <button
            class="btn-primary"
            on:click={createAPIToken}
            disabled={creatingToken || !newTokenName.trim()}
          >
            {creatingToken ? "Creating..." : "Create Token"}
          </button>
        </div>
      {/if}
    </div>
  </div>
{/if}