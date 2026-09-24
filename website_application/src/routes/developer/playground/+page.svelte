<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { page } from "$app/stores";
  import { resolve } from "$app/paths";
  import { auth } from "$lib/stores/auth";
  import SkeletonLoader from "$lib/components/SkeletonLoader.svelte";
  import GraphQLExplorer from "$lib/components/GraphQLExplorer.svelte";
  import { Code2, LogIn, Bot, Package } from "lucide-svelte";
  import { getDocsSiteUrl, getMcpEndpoint } from "$lib/config";
  import { Button } from "$lib/components/ui/button";

  const mcpDocsUrl = `${getDocsSiteUrl().replace(/\/$/, "")}/agents/mcp`;

  let isAuthenticated = $state(false);
  const authToken = "YOUR_API_TOKEN";
  let booted = $state(false);

  const initialQuery = $derived($page.url.searchParams.get("query") ?? "");

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
    booted = true;
  });
</script>

<svelte:head>
  <title>GraphQL Playground - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col">
  <div class="px-4 sm:px-6 lg:px-8 py-3 border-b border-border shrink-0">
    <div class="flex justify-between items-center gap-4">
      <div class="flex items-center gap-3">
        <Code2 class="w-5 h-5 text-primary" />
        <h1 class="text-lg font-bold text-foreground">GraphQL Playground</h1>
      </div>

      <div class="flex items-center gap-4 text-xs">
        <div class="hidden md:flex items-center gap-4">
          <div class="flex items-center gap-1.5">
            <span class="text-muted-foreground">HTTP</span>
            <code class="font-mono text-foreground bg-muted px-1.5 py-0.5"
              >{(import.meta as ImportMeta & { env: Record<string, string> }).env
                .VITE_GRAPHQL_HTTP_URL}</code
            >
          </div>
          <div class="flex items-center gap-1.5">
            <span class="text-muted-foreground">WS</span>
            <code class="font-mono text-foreground bg-muted px-1.5 py-0.5"
              >{(import.meta as ImportMeta & { env: Record<string, string> }).env
                .VITE_GRAPHQL_WS_URL}</code
            >
          </div>
          <!-- eslint-disable svelte/no-navigation-without-resolve -->
          <a
            href={mcpDocsUrl}
            target="_blank"
            rel="noopener noreferrer"
            class="flex items-center gap-1.5 hover:text-primary transition-colors"
            title="MCP endpoint for AI agents: open the MCP docs"
          >
            <Bot class="w-3.5 h-3.5 text-success" />
            <span class="text-muted-foreground hover:text-primary">MCP</span>
            <code class="font-mono text-foreground bg-muted px-1.5 py-0.5">{getMcpEndpoint()}</code>
          </a>
          <!-- eslint-enable svelte/no-navigation-without-resolve -->
        </div>
        <a
          href={resolve("/developer/sdks")}
          class="flex items-center gap-1.5 text-muted-foreground hover:text-primary transition-colors"
          title="TypeScript, Go, and Python SDKs"
        >
          <Package class="w-3.5 h-3.5" />
          <span>SDKs</span>
        </a>
        {#if !isAuthenticated}
          <Button href={resolve("/login")} size="sm" class="gap-2">
            <LogIn class="w-4 h-4" />
            Login
          </Button>
        {/if}
      </div>
    </div>
  </div>

  {#if !booted}
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
        <p class="text-muted-foreground mb-6">
          Sign in to use the GraphQL playground. Create an API token at
          <a href={resolve("/developer/api-keys")} class="text-primary hover:underline"
            >/developer/api-keys</a
          >.
        </p>
        <Button href={resolve("/login")}>Sign In</Button>
      </div>
    </div>
  {:else}
    <div class="flex-1 overflow-hidden">
      <GraphQLExplorer {authToken} {initialQuery} />
    </div>
  {/if}
</div>
