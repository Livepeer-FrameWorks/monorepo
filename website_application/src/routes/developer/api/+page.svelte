<script>
  import { onMount } from "svelte";
  import { base } from "$app/paths";
  import { auth } from "$lib/stores/auth";
  import { developerService } from "$lib/graphql/services/developer.js";
  import { toast } from "$lib/stores/toast.js";
  import SkeletonLoader from "$lib/components/SkeletonLoader.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";

  let isAuthenticated = false;
  /** @type {any} */
  let user = null;
  let loading = true;

  // Disabled REST API testing (endpoints migrated to GraphQL)
  let selectedEndpoint = null;
  let apiResponse = "REST API endpoints have been migrated to GraphQL. Use the GraphQL playground instead.";
  let requestBody = "";
  let testingInProgress = false;

  // API Token Management
  let apiTokens = [];
  let showCreateTokenModal = false;
  let creatingToken = false;
  let newTokenName = "";
  let newTokenExpiry = 0; // 0 = never expires
  let newlyCreatedToken = null;

  // API endpoints documentation
  const apiSections = [
    {
      title: "Stream Management",
      icon: "🎥",
      endpoints: [
        {
          method: "GET",
          path: "/api/streams",
          description: "List all user streams with detailed information",
          response: `[
  {
    "id": "stream-id",
    "user_id": "user-id",
    "stream_key": "sk_...",
    "playback_id": "pb_...",
    "internal_name": "internal-uuid",
    "title": "My Live Stream",
    "description": "Stream description",
    "status": "live",
    "viewers": 42,
    "resolution": "1920x1080",
    "bitrate": "2500 kbps",
    "is_recording_enabled": false,
    "is_public": true,
    "max_viewers": 156,
    "created_at": "2025-01-24T10:00:00Z",
    "updated_at": "2025-01-24T11:30:00Z"
  }
]`,
          requiresAuth: true,
        },
        {
          method: "POST",
          path: "/api/streams",
          description: "Create a new stream",
          body: `{
  "title": "My New Stream",
  "description": "Optional description"
}`,
          response: `{
  "id": "new-stream-id",
  "stream_key": "sk_new_key",
  "playback_id": "pb_new_id",
  "internal_name": "internal-uuid",
  "title": "My New Stream",
  "description": "Optional description",
  "status": "offline",
  "ingest_url": "rtmp://localhost:1935/live/sk_new_key",
  "playback_url": "https://localhost:9080/hls/pb_new_id.m3u8"
}`,
          requiresAuth: true,
        },
        {
          method: "GET",
          path: "/api/streams/:id",
          description: "Get specific stream details",
          response: `{
  "id": "stream-id",
  "user_id": "user-id",
  "stream_key": "sk_...",
  "title": "My Stream",
  "status": "live",
  "viewers": 42,
  "start_time": "2025-01-24T10:00:00Z",
  "end_time": null,
  "bitrate": "2500 kbps",
  "resolution": "1920x1080",
  "created_at": "2025-01-24T09:00:00Z",
  "updated_at": "2025-01-24T11:30:00Z"
}`,
          requiresAuth: true,
        },
        {
          method: "DELETE",
          path: "/api/streams/:id",
          description: "Delete a stream permanently",
          response: `{
  "message": "Stream deleted successfully",
  "stream_id": "stream-id",
  "stream_title": "My Stream",
  "deleted_at": "2025-01-24T11:30:00Z"
}`,
          requiresAuth: true,
        },
        {
          method: "GET",
          path: "/api/streams/:id/metrics",
          description: "Get real-time metrics for a specific stream",
          response: `{
  "viewers": 42,
  "status": "live",
  "bandwidth_in": 2500000,
  "bandwidth_out": 5000000,
  "resolution": "1920x1080",
  "bitrate": "2500 kbps",
  "max_viewers": 156,
  "updated_at": "2025-01-24T11:30:00Z"
}`,
          requiresAuth: true,
        },
        {
          method: "POST",
          path: "/api/streams/:id/refresh-key",
          description: "Generate new stream key for security",
          response: `{
  "message": "Stream key refreshed successfully",
  "stream_id": "stream-id",
  "stream_key": "sk_new_refreshed_key",
  "playback_id": "pb_same_id",
  "ingest_url": "rtmp://localhost:1935/live/sk_new_refreshed_key",
  "old_key_invalidated": true
}`,
          requiresAuth: true,
        },
      ],
    },
    {
      title: "Analytics",
      icon: "📊",
      endpoints: [
        {
          method: "GET",
          path: "/api/streams/:id/embed",
          description: "Get embed code and playback URLs for a stream",
          response: `{
  "embed_code": "<iframe src='https://localhost:9080/embed/pb_id' frameborder='0' allowfullscreen></iframe>",
  "playback_id": "pb_id",
  "hls_url": "https://localhost:9080/hls/pb_id.m3u8",
  "webrtc_url": "https://localhost:9080/webrtc/pb_id"
}`,
          requiresAuth: true,
        },
      ],
    },
    {
      title: "User Info",
      icon: "👤",
      endpoints: [
        {
          method: "GET",
          path: "/api/me",
          description: "Get current user profile and streams",
          response: `{
  "user": {
    "id": "user-id",
    "email": "user@example.com",
    "created_at": "2025-01-24T10:00:00Z",
    "is_active": true
  },
  "streams": [
    {
      "id": "stream-id",
      "stream_key": "sk_...",
      "playback_id": "pb_...",
      "title": "My Stream",
      "status": "offline",
      "viewers": 0
    }
  ]
}`,
          requiresAuth: true,
        },
      ],
    },
    {
      title: "Developer Tokens",
      icon: "🔑",
      endpoints: [
        {
          method: "POST",
          path: "/api/developer/tokens",
          description: "Create a new API access token",
          body: `{
  "token_name": "My App Token",
  "permissions": "read,write",
  "expires_in": 365
}`,
          response: `{
  "id": "token-id",
  "token_value": "at_1234567890abcdef...",
  "token_name": "My App Token",
  "permissions": "read,write",
  "expires_at": "2026-01-24T10:00:00Z",
  "created_at": "2025-01-24T10:00:00Z",
  "message": "API token created successfully. Store this token securely - it won't be shown again."
}`,
          requiresAuth: true,
        },
        {
          method: "GET",
          path: "/api/developer/tokens",
          description: "List all your API tokens (without values)",
          response: `{
  "tokens": [
    {
      "id": "token-id",
      "token_name": "My App Token",
      "permissions": "read,write",
      "status": "active",
      "last_used_at": "2025-01-24T11:00:00Z",
      "expires_at": "2026-01-24T10:00:00Z",
      "created_at": "2025-01-24T10:00:00Z"
    }
  ],
  "count": 1
}`,
          requiresAuth: true,
        },
        {
          method: "DELETE",
          path: "/api/developer/tokens/:id",
          description: "Revoke an API token",
          response: `{
  "message": "API token revoked successfully",
  "token_id": "token-id",
  "token_name": "My App Token",
  "revoked_at": "2025-01-24T11:30:00Z"
}`,
          requiresAuth: true,
        },
      ],
    },
  ];

  // Subscribe to auth store
  auth.subscribe((authState) => {
    isAuthenticated = authState.isAuthenticated;
    user = authState.user?.user || null;
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

  async function testEndpoint(endpoint) {
    if (!apiTokens.length) {
      apiResponse = `Error: No API tokens available. Please create an API token first to test endpoints.

To create a token:
1. Click "Create New Token" button
2. Give it a name (e.g., "Testing Token")
3. Copy the generated token
4. Use it in the Authorization header as: Bearer at_your_token_here`;
      return;
    }

    const activeToken = apiTokens.find((t) => t.status === "active");
    if (!activeToken) {
      apiResponse = `Error: No active API tokens available. Please create a new API token.`;
      return;
    }

    try {
      testingInProgress = true;

      const headers = {
        "Content-Type": "application/json",
        Authorization: `Bearer ${
          activeToken.token || "at_your_token_here"
        }`,
      };

      const options = {
        method: endpoint.method,
        headers,
      };

      if (endpoint.method === "POST" && requestBody) {
        try {
          JSON.parse(requestBody); // Validate JSON
          options.body = requestBody;
        } catch (e) {
          apiResponse = `Error: Invalid JSON in request body\n${e.message}`;
          return;
        }
      }

      // Replace :id with example ID for demo
      let url = endpoint.path.replace(":id", "example-stream-id");

      // Make actual API call
      const response = await fetch(`${API_URL}${url}`, options);
      const responseData = await response.text();

      let statusColor = response.ok ? "✅" : "❌";

      apiResponse = `${statusColor} ${endpoint.method} ${url}
Status: ${response.status} ${response.statusText}

Request Headers:
${JSON.stringify(headers, null, 2)}

${
  endpoint.method === "POST" && requestBody
    ? `Request Body:
${requestBody}

`
    : ""
}Response:
${responseData}

${
  !response.ok
    ? `\nNote: This might be expected if the endpoint requires specific data or if you don't have the referenced resource.`
    : ""
}`;
    } catch (error) {
      apiResponse = `❌ Error making request:
${error.message}

This could be due to:
- Network connectivity issues
- CORS restrictions
- Server not running
- Invalid endpoint URL`;
    } finally {
      testingInProgress = false;
    }
  }

  function selectEndpoint(endpoint) {
    selectedEndpoint = endpoint;
    requestBody = endpoint.body || "";
    apiResponse = "";
  }

  function copyToClipboard(text) {
    navigator.clipboard.writeText(text);
  }

  function getMethodColor(method) {
    switch (method) {
      case "GET":
        return "text-tokyo-night-green";
      case "POST":
        return "text-tokyo-night-blue";
      case "PUT":
        return "text-tokyo-night-yellow";
      case "DELETE":
        return "text-tokyo-night-red";
      default:
        return "text-tokyo-night-comment";
    }
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
  <title>Developer API - FrameWorks</title>
</svelte:head>

<div class="space-y-8 page-transition">
  <!-- Page Header -->
  <div class="flex justify-between items-start">
    <div>
      <h1 class="text-3xl font-bold text-tokyo-night-fg mb-2">
        🛠️ Developer API
      </h1>
      <p class="text-tokyo-night-fg-dark">
        Generate API tokens, test endpoints, and integrate FrameWorks into your
        applications
      </p>
    </div>

    <div class="flex space-x-3">
      {#if isAuthenticated}
        <button
          class="btn-primary"
          on:click={() => {
            showCreateTokenModal = true;
            newlyCreatedToken = null;
          }}
        >
          <span class="mr-2">🔑</span>
          Create New Token
        </button>
      {:else}
        <a href="{base}/login" class="btn-primary">
          <span class="mr-2">🔐</span>
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
      <div class="text-6xl mb-4">🔐</div>
      <h3 class="text-xl font-semibold text-tokyo-night-fg mb-2">
        Authentication Required
      </h3>
      <p class="text-tokyo-night-fg-dark mb-6">
        Please sign in to access the API documentation and manage your API keys.
      </p>
      <a href="{base}/login" class="btn-primary"> Sign In </a>
    </div>
  {:else}
    <!-- API Overview -->
    <div class="grid grid-cols-1 md:grid-cols-3 gap-6">
      <div class="glow-card p-6">
        <div class="flex items-center justify-between">
          <div>
            <p class="text-sm text-tokyo-night-comment">Base URL</p>
            <p class="text-lg font-mono text-tokyo-night-fg">
              {API_URL}/api
            </p>
          </div>
          <span class="text-2xl">🌐</span>
        </div>
      </div>

      <div class="glow-card p-6">
        <div class="flex items-center justify-between">
          <div>
            <p class="text-sm text-tokyo-night-comment">Authentication</p>
            <p class="text-lg font-semibold text-tokyo-night-fg">API Token</p>
          </div>
          <span class="text-2xl">🔑</span>
        </div>
      </div>

      <div class="glow-card p-6">
        <div class="flex items-center justify-between">
          <div>
            <p class="text-sm text-tokyo-night-comment">Active Tokens</p>
            <p class="text-lg font-semibold text-tokyo-night-fg">
              {apiTokens.filter((t) => t.status === "active").length}
            </p>
          </div>
          <span class="text-2xl">⚡</span>
        </div>
      </div>
    </div>

    <!-- API Token Management -->
    <div class="card">
      <div class="card-header">
        <h2 class="text-xl font-semibold text-tokyo-night-fg mb-2">
          🔑 Your API Tokens
        </h2>
        <p class="text-tokyo-night-fg-dark">
          Generate and manage API tokens for programmatic access to your streams
        </p>
      </div>

      {#if apiTokens.length === 0}
        <div class="text-center py-8">
          <div class="text-4xl mb-4">🔑</div>
          <h3 class="text-lg font-semibold text-tokyo-night-fg mb-2">
            No API Tokens
          </h3>
          <p class="text-tokyo-night-comment mb-4">
            Create your first API token to start using the FrameWorks API
          </p>
          <button
            class="btn-primary"
            on:click={() => {
              showCreateTokenModal = true;
              newlyCreatedToken = null;
            }}
          >
            <span class="mr-2">➕</span>
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

    <!-- Interactive API Explorer -->
    <div class="grid grid-cols-1 xl:grid-cols-2 gap-8">
      <!-- Endpoint List -->
      <div class="card">
        <div class="card-header">
          <h2 class="text-xl font-semibold text-tokyo-night-fg mb-2">
            🔗 API Endpoints
          </h2>
          <p class="text-tokyo-night-fg-dark">
            Click any endpoint to test it with your API tokens
          </p>
        </div>

        <div class="space-y-6">
          {#each apiSections as section}
            <div>
              <h3
                class="flex items-center space-x-2 font-semibold text-tokyo-night-fg mb-3"
              >
                <span>{section.icon}</span>
                <span>{section.title}</span>
              </h3>

              <div class="space-y-2">
                {#each section.endpoints as endpoint}
                  <button
                    on:click={() => selectEndpoint(endpoint)}
                    class="w-full text-left p-3 rounded-lg border border-tokyo-night-fg-gutter hover:bg-tokyo-night-bg-highlight transition-colors {selectedEndpoint ===
                    endpoint
                      ? 'bg-tokyo-night-bg-highlight border-tokyo-night-blue'
                      : ''}"
                  >
                    <div class="flex items-center justify-between mb-1">
                      <div class="flex items-center space-x-3">
                        <span
                          class="text-xs font-mono px-2 py-1 rounded {getMethodColor(
                            endpoint.method
                          )} bg-tokyo-night-bg"
                        >
                          {endpoint.method}
                        </span>
                        <span class="font-mono text-sm text-tokyo-night-fg">
                          {endpoint.path}
                        </span>
                      </div>
                      <span class="text-xs text-tokyo-night-comment"
                        >🔒 Token</span
                      >
                    </div>
                    <p class="text-xs text-tokyo-night-comment">
                      {endpoint.description}
                    </p>
                  </button>
                {/each}
              </div>
            </div>
          {/each}
        </div>
      </div>

      <!-- API Tester -->
      <div class="card">
        <div class="card-header">
          <h2 class="text-xl font-semibold text-tokyo-night-fg mb-2">
            🧪 Live API Tester
          </h2>
          <p class="text-tokyo-night-fg-dark">
            Test endpoints with real API calls using your tokens
          </p>
        </div>

        {#if selectedEndpoint}
          <div class="space-y-4">
            <!-- Request Body (for POST endpoints) -->
            {#if selectedEndpoint.method === "POST"}
              <div>
                <label
                  class="block text-sm font-medium text-tokyo-night-fg mb-2"
                >
                  Request Body (JSON)
                </label>
                <textarea
                  bind:value={requestBody}
                  class="input w-full h-32 font-mono text-sm"
                  placeholder="Enter JSON request body..."
                />
              </div>
            {/if}

            <!-- Test Button -->
            <button
              on:click={() => testEndpoint(selectedEndpoint)}
              class="btn-primary w-full"
              disabled={testingInProgress}
            >
              <span class="mr-2">{testingInProgress ? "⏳" : "🚀"}</span>
              {testingInProgress
                ? "Testing..."
                : `Test ${selectedEndpoint.method} ${selectedEndpoint.path}`}
            </button>

            <!-- Response -->
            {#if apiResponse}
              <div>
                <label
                  class="block text-sm font-medium text-tokyo-night-fg mb-2"
                >
                  Response
                </label>
                <div class="relative">
                  <pre
                    class="bg-tokyo-night-bg p-4 rounded-lg text-sm font-mono text-tokyo-night-fg overflow-x-auto border border-tokyo-night-fg-gutter max-h-96 overflow-y-auto">{apiResponse}</pre>
                  <button
                    on:click={() => copyToClipboard(apiResponse)}
                    class="absolute top-2 right-2 btn-secondary text-xs px-2 py-1"
                  >
                    📋
                  </button>
                </div>
              </div>
            {/if}
          </div>
        {:else}
          <div class="text-center py-12">
            <div class="text-4xl mb-4">🔗</div>
            <h3 class="text-lg font-semibold text-tokyo-night-fg mb-2">
              Select an Endpoint
            </h3>
            <p class="text-tokyo-night-comment">
              Choose an endpoint from the list to test it with live API calls
            </p>
          </div>
        {/if}
      </div>
    </div>

    <!-- Code Examples -->
    <div class="card">
      <div class="card-header">
        <h2 class="text-xl font-semibold text-tokyo-night-fg mb-2">
          💻 Code Examples
        </h2>
        <p class="text-tokyo-night-fg-dark">
          Ready-to-use code snippets with your API tokens
        </p>
      </div>

      <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
        <!-- JavaScript Example -->
        <div>
          <h3 class="font-semibold text-tokyo-night-fg mb-3">
            JavaScript (Fetch)
          </h3>
          <div class="relative">
            <pre
              class="bg-tokyo-night-bg p-4 rounded-lg text-sm font-mono text-tokyo-night-fg overflow-x-auto"><code
                >{`// Using your API token
const API_TOKEN = '${
                  apiTokens.find((t) => t.status === "active")?.token ||
                  "at_your_token_here"
                }';

// Get all streams
const streams = await fetch('${API_URL}/api/streams', {
  headers: { 
    'Authorization': \`Bearer \${API_TOKEN}\`
  }
});

const streamData = await streams.json();
console.log(streamData);

// Create a new stream
const newStream = await fetch('${API_URL}/api/streams', {
  method: 'POST',
  headers: {
    'Authorization': \`Bearer \${API_TOKEN}\`,
    'Content-Type': 'application/json'
  },
  body: JSON.stringify({
    title: 'My New Stream',
    description: 'Created via API'
  })
});`}</code
              ></pre>
            <button
              on:click={() =>
                copyToClipboard(`// Using your API token
const API_TOKEN = '${
                  apiTokens.find((t) => t.status === "active")?.token ||
                  "at_your_token_here"
                }';

// Get all streams
const streams = await fetch('${API_URL}/api/streams', {
  headers: { 
    'Authorization': \`Bearer \${API_TOKEN}\`
  }
});

const streamData = await streams.json();
console.log(streamData);

// Create a new stream
const newStream = await fetch('${API_URL}/api/streams', {
  method: 'POST',
  headers: {
    'Authorization': \`Bearer \${API_TOKEN}\`,
    'Content-Type': 'application/json'
  },
  body: JSON.stringify({
    title: 'My New Stream',
    description: 'Created via API'
  })
});`)}
              class="absolute top-2 right-2 btn-secondary text-xs px-2 py-1"
            >
              📋
            </button>
          </div>
        </div>

        <!-- cURL Example -->
        <div>
          <h3 class="font-semibold text-tokyo-night-fg mb-3">cURL</h3>
          <div class="relative">
            <pre
              class="bg-tokyo-night-bg p-4 rounded-lg text-sm font-mono text-tokyo-night-fg overflow-x-auto"><code
                >{`# Get all streams
curl -X GET ${API_URL}/api/streams \\
  -H "Authorization: Bearer ${
    apiTokens.find((t) => t.status === "active")?.token ||
    "at_your_token_here"
  }"

# Create new stream
curl -X POST ${API_URL}/api/streams \\
  -H "Authorization: Bearer ${
    apiTokens.find((t) => t.status === "active")?.token ||
    "at_your_token_here"
  }" \\
  -H "Content-Type: application/json" \\
  -d '{"title":"My New Stream","description":"Created via cURL"}'

# Get stream metrics
curl -X GET ${API_URL}/api/streams/STREAM_ID/metrics \\
  -H "Authorization: Bearer ${
    apiTokens.find((t) => t.status === "active")?.token ||
    "at_your_token_here"
  }"`}</code
              ></pre>
            <button
              on:click={() =>
                copyToClipboard(`# Get all streams
curl -X GET ${API_URL}/api/streams \\
  -H "Authorization: Bearer ${
    apiTokens.find((t) => t.status === "active")?.token ||
    "at_your_token_here"
  }"

# Create new stream
curl -X POST ${API_URL}/api/streams \\
  -H "Authorization: Bearer ${
    apiTokens.find((t) => t.status === "active")?.token ||
    "at_your_token_here"
  }" \\
  -H "Content-Type: application/json" \\
  -d '{"title":"My New Stream","description":"Created via cURL"}'

# Get stream metrics
curl -X GET ${API_URL}/api/streams/STREAM_ID/metrics \\
  -H "Authorization: Bearer ${
    apiTokens.find((t) => t.status === "active")?.token ||
    "at_your_token_here"
  }"`)}
              class="absolute top-2 right-2 btn-secondary text-xs px-2 py-1"
            >
              📋
            </button>
          </div>
        </div>
      </div>
    </div>

    <!-- Authentication Guide -->
    <div class="card">
      <div class="card-header">
        <h2 class="text-xl font-semibold text-tokyo-night-fg mb-2">
          🔐 API Authentication Guide
        </h2>
        <p class="text-tokyo-night-fg-dark">
          How to authenticate with the FrameWorks API using tokens
        </p>
      </div>

      <div class="grid grid-cols-1 md:grid-cols-3 gap-6">
        <div class="text-center">
          <div class="text-3xl mb-3">1️⃣</div>
          <h3 class="font-semibold text-tokyo-night-fg mb-2">Generate Token</h3>
          <p class="text-sm text-tokyo-night-comment">
            Create an API token from this page. Tokens work like stream keys and
            don't expire unless you set an expiration.
          </p>
        </div>

        <div class="text-center">
          <div class="text-3xl mb-3">2️⃣</div>
          <h3 class="font-semibold text-tokyo-night-fg mb-2">Include Header</h3>
          <p class="text-sm text-tokyo-night-comment">
            Add "Authorization: Bearer at_your_token" header to all API
            requests. Tokens start with "at_".
          </p>
        </div>

        <div class="text-center">
          <div class="text-3xl mb-3">3️⃣</div>
          <h3 class="font-semibold text-tokyo-night-fg mb-2">Manage Tokens</h3>
          <p class="text-sm text-tokyo-night-comment">
            Revoke tokens you no longer need. Create separate tokens for
            different applications or environments.
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
        <h3 class="text-xl font-semibold text-tokyo-night-green mb-4">
          🎉 Token Created Successfully!
        </h3>

        <div class="space-y-4">
          <div>
            <label
              class="block text-sm font-medium text-tokyo-night-fg-dark mb-2"
            >
              Token Name
            </label>
            <p class="text-tokyo-night-fg font-semibold">
              {newlyCreatedToken.token_name}
            </p>
          </div>

          <div>
            <label
              class="block text-sm font-medium text-tokyo-night-fg-dark mb-2"
            >
              API Token (Copy this now - it won't be shown again!)
            </label>
            <div class="flex space-x-2">
              <input
                type="text"
                value={newlyCreatedToken.token_value}
                readonly
                class="input flex-1 font-mono text-sm bg-tokyo-night-bg-highlight"
              />
              <button
                on:click={() => copyToClipboard(newlyCreatedToken.token_value)}
                class="btn-primary"
              >
                📋 Copy
              </button>
            </div>
          </div>

          <div
            class="bg-tokyo-night-bg-highlight p-3 rounded border border-tokyo-night-yellow"
          >
            <p class="text-sm text-tokyo-night-yellow">
              ⚠️ <strong>Important:</strong> Store this token securely. You won't
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
              💡 <strong>Tip:</strong> Create separate tokens for different applications
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
