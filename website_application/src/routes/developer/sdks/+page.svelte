<script lang="ts">
  import { Button } from "$lib/components/ui/button";
  import { GridSeam } from "$lib/components/layout";
  import { getIconComponent } from "$lib/iconUtils";
  import { getDocsSiteUrl, getGithubUrl, getGraphqlHttpUrl, getMcpEndpoint, getRtmpServerUrl } from "$lib/config";

  type Framework = "react" | "svelte" | "vanilla";

  let selectedFramework = $state<Framework>("react");

  const docsSiteUrl = getDocsSiteUrl();
  const docsBaseUrl = docsSiteUrl.replace(/\/$/, "");
  const githubBaseUrl = getGithubUrl().replace(/\/$/, "");
  const graphqlUrl = getGraphqlHttpUrl();
  const rtmpUrl = getRtmpServerUrl();
  const mcpEndpoint = getMcpEndpoint();

  // SDK definitions
  const playerSdk = {
    name: "Player SDK",
    description: "Adaptive video player with HLS, DASH, WebRTC, and WebCodecs support. Automatic protocol selection based on stream type and browser capabilities.",
    icon: "Play",
    packages: {
      core: "@livepeer-frameworks/player-core",
      react: "@livepeer-frameworks/player-react",
      svelte: "@livepeer-frameworks/player-svelte",
    },
    version: "0.0.5",
    features: [
      "HLS.js, DASH.js, and native playback",
      "WebRTC for ultra-low latency",
      "WebCodecs for advanced decoding",
      "Automatic quality adaptation",
      "Built-in controls with customization",
      "Picture-in-Picture support",
    ],
    codeExamples: {
      react: `import { FrameworksPlayer } from '@livepeer-frameworks/player-react';
import '@livepeer-frameworks/player-core/player.css';

function App() {
  return (
    <FrameworksPlayer
      playbackId="your-playback-id"
      gatewayUrl="${graphqlUrl}"
    />
  );
}`,
      svelte: `<script>
  import { FrameworksPlayer } from '@livepeer-frameworks/player-svelte';
  import '@livepeer-frameworks/player-core/player.css';
<\/script>

<FrameworksPlayer
  playbackId="your-playback-id"
  gatewayUrl="${graphqlUrl}"
/>`,
      vanilla: `import { FrameWorksPlayer } from '@livepeer-frameworks/player-core/vanilla';
import '@livepeer-frameworks/player-core/player.css';

const player = new FrameWorksPlayer('#player-container', {
  playbackId: 'your-playback-id',
  gatewayUrl: '${graphqlUrl}',
});

// When done:
player.destroy();`,
    },
    docsUrl: `${docsBaseUrl}/player`,
    githubUrl: `${githubBaseUrl}/tree/main/npm_player`,
  };

  const studioSdk = {
    name: "Studio SDK",
    description: "Browser-based streaming with WebCodecs encoding. Stream directly from camera/microphone or screen share with real-time preview and scene management.",
    icon: "Radio",
    packages: {
      core: "@livepeer-frameworks/streamcrafter-core",
      react: "@livepeer-frameworks/streamcrafter-react",
      svelte: "@livepeer-frameworks/streamcrafter-svelte",
    },
    version: "0.0.3",
    features: [
      "WebCodecs hardware encoding",
      "WHIP ingest protocol",
      "Camera, mic, and screen capture",
      "Scene and layer management",
      "Real-time audio mixing",
      "Preview before going live",
    ],
    codeExamples: {
      react: `import { StreamCrafter, Preview, Controls } from '@livepeer-frameworks/streamcrafter-react';
import '@livepeer-frameworks/streamcrafter-core/streamcrafter.css';

function Studio() {
  return (
    <StreamCrafter
      streamKey="your-stream-key"
      ingestUrl="${rtmpUrl}"
    >
      <Preview />
      <Controls />
    </StreamCrafter>
  );
}`,
      svelte: `<script>
  import { StreamCrafter, Preview, Controls } from '@livepeer-frameworks/streamcrafter-svelte';
  import '@livepeer-frameworks/streamcrafter-core/streamcrafter.css';
<\/script>

<StreamCrafter
  streamKey="your-stream-key"
  ingestUrl="${rtmpUrl}"
>
  <Preview />
  <Controls />
</StreamCrafter>`,
      vanilla: `import { IngestController } from '@livepeer-frameworks/streamcrafter-core/vanilla';
import '@livepeer-frameworks/streamcrafter-core/streamcrafter.css';

const controller = new IngestController({
  streamKey: 'your-stream-key',
  ingestUrl: '${rtmpUrl}',
});

// Start camera
await controller.addCameraSource();

// Go live
await controller.startStreaming();

// Stop streaming
await controller.stopStreaming();`,
    },
    docsUrl: `${docsBaseUrl}/studio`,
    githubUrl: `${githubBaseUrl}/tree/main/npm_studio`,
  };

  const mcpServer = {
    name: "MCP Server",
    description: "AI agent integration via Model Context Protocol. Let AI assistants manage streams, check analytics, handle billing, and automate operations through any MCP-compatible client.",
    icon: "Bot",
    endpoint: mcpEndpoint,
    features: [
      "Stream lifecycle management",
      "Billing and account status",
      "Real-time analytics queries",
      "Guided workflows via prompts",
      "Wallet-based agent auth",
      "x402 machine payments",
    ],
    docsUrl: `${docsBaseUrl}/streamers/mcp`,
    specUrl: "https://modelcontextprotocol.io",
  };

  const upcomingSdks = [
    {
      name: "API Client SDK",
      description: "Type-safe GraphQL client with built-in authentication and pagination helpers.",
      icon: "Code2",
      status: "Coming Soon",
    },
    {
      name: "Webhook SDK",
      description: "Type-safe webhook handlers for stream lifecycle, artifacts, and viewer events.",
      icon: "Webhook",
      status: "Coming Soon",
    },
    {
      name: "CLI Tools",
      description: "Command-line tools for stream management, analytics, and deployment automation.",
      icon: "Terminal",
      status: "Coming Soon",
    },
  ];

  function getFrameworkInstall(packages: Record<string, string>, framework: Framework): string {
    if (framework === "vanilla") {
      return `npm install ${packages.core}`;
    }
    return `npm install ${packages[framework]} ${packages.core}`;
  }

  function copyToClipboard(text: string) {
    navigator.clipboard.writeText(text);
  }

  // Icons
  const PackageIcon = getIconComponent("Package");
  const PlayIcon = getIconComponent("Play");
  const RadioIcon = getIconComponent("Radio");
  const Code2Icon = getIconComponent("Code2");
  const TerminalIcon = getIconComponent("Terminal");
  const WebhookIcon = getIconComponent("Webhook");
  const BotIcon = getIconComponent("Bot");
  const CopyIcon = getIconComponent("Copy");
  const ExternalLinkIcon = getIconComponent("ExternalLink");
  const BookOpenIcon = getIconComponent("BookOpen");
  const GithubIcon = getIconComponent("Github");
  const CheckIcon = getIconComponent("Check");
  const ClockIcon = getIconComponent("Clock");
  const ZapIcon = getIconComponent("Zap");

  function getIconByName(name: string) {
    switch (name) {
      case "Play": return PlayIcon;
      case "Radio": return RadioIcon;
      case "Code2": return Code2Icon;
      case "Terminal": return TerminalIcon;
      case "Webhook": return WebhookIcon;
      case "Bot": return BotIcon;
      default: return PackageIcon;
    }
  }
</script>

<svelte:head>
  <title>SDKs & Libraries - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col overflow-hidden">
  <!-- Fixed Page Header -->
  <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0 z-10 bg-background">
    <div class="flex justify-between items-center">
      <div class="flex items-center gap-3">
        <PackageIcon class="w-5 h-5 text-primary" />
        <div>
          <h1 class="text-xl font-bold text-foreground">SDKs & Libraries</h1>
          <p class="text-sm text-muted-foreground">
            Player and Studio SDKs for React, Svelte, and vanilla JavaScript
          </p>
        </div>
      </div>
    </div>
  </div>

  <!-- Scrollable Content -->
  <div class="flex-1 overflow-y-auto bg-background/50">
    <div class="page-transition">
      <!-- Framework Selector -->
      <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] bg-muted/30">
        <div class="flex items-center gap-4">
          <span class="text-sm font-medium text-muted-foreground">Framework:</span>
          <div class="flex border border-border rounded-md overflow-hidden">
            <button
              type="button"
              class="px-4 py-2 text-sm font-medium transition-colors {selectedFramework === 'react' ? 'bg-primary text-primary-foreground' : 'bg-muted/30 text-muted-foreground hover:bg-muted/50'}"
              onclick={() => selectedFramework = 'react'}
            >
              React
            </button>
            <button
              type="button"
              class="px-4 py-2 text-sm font-medium transition-colors border-x border-border {selectedFramework === 'svelte' ? 'bg-primary text-primary-foreground' : 'bg-muted/30 text-muted-foreground hover:bg-muted/50'}"
              onclick={() => selectedFramework = 'svelte'}
            >
              Svelte
            </button>
            <button
              type="button"
              class="px-4 py-2 text-sm font-medium transition-colors {selectedFramework === 'vanilla' ? 'bg-primary text-primary-foreground' : 'bg-muted/30 text-muted-foreground hover:bg-muted/50'}"
              onclick={() => selectedFramework = 'vanilla'}
            >
              Vanilla JS
            </button>
          </div>
        </div>
      </div>

      <div class="dashboard-grid p-0">
        <!-- Player SDK -->
        <div class="slab col-span-full">
          <div class="slab-header flex items-center justify-between">
            <div class="flex items-center gap-3">
              <div class="w-10 h-10 rounded-lg bg-primary/10 flex items-center justify-center">
                <PlayIcon class="w-5 h-5 text-primary" />
              </div>
              <div>
                <h3 class="font-semibold text-foreground">{playerSdk.name}</h3>
                <span class="text-xs text-muted-foreground">v{playerSdk.version}</span>
              </div>
            </div>
            <div class="flex items-center gap-2">
              <Button
                variant="outline"
                size="sm"
                class="gap-2 h-8"
                href={playerSdk.docsUrl}
                target="_blank"
              >
                <BookOpenIcon class="w-3.5 h-3.5" />
                Docs
              </Button>
              <Button
                variant="outline"
                size="sm"
                class="gap-2 h-8"
                href={playerSdk.githubUrl}
                target="_blank"
              >
                <GithubIcon class="w-3.5 h-3.5" />
                GitHub
              </Button>
            </div>
          </div>
          <div class="slab-body--padded space-y-6">
            <p class="text-sm text-muted-foreground">{playerSdk.description}</p>

            <!-- Features -->
            <div>
              <h4 class="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-3">Features</h4>
              <div class="grid grid-cols-2 md:grid-cols-3 gap-2">
                {#each playerSdk.features as feature}
                  <div class="flex items-center gap-2 text-sm text-foreground">
                    <CheckIcon class="w-3.5 h-3.5 text-success shrink-0" />
                    <span>{feature}</span>
                  </div>
                {/each}
              </div>
            </div>

            <!-- Install -->
            <div>
              <h4 class="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-3">Installation</h4>
              <div class="relative bg-muted/50 border border-border rounded-md p-3 font-mono text-sm">
                <code class="text-foreground">{getFrameworkInstall(playerSdk.packages, selectedFramework)}</code>
                <Button
                  variant="ghost"
                  size="sm"
                  class="absolute right-2 top-2 h-7 w-7 p-0"
                  onclick={() => copyToClipboard(getFrameworkInstall(playerSdk.packages, selectedFramework))}
                >
                  <CopyIcon class="w-3.5 h-3.5" />
                </Button>
              </div>
            </div>

            <!-- Quick Start -->
            <div>
              <h4 class="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-3">Quick Start</h4>
              <div class="relative bg-muted/50 border border-border rounded-md p-4 overflow-x-auto">
                <pre class="text-sm font-mono text-foreground whitespace-pre"><code>{playerSdk.codeExamples[selectedFramework]}</code></pre>
                <Button
                  variant="ghost"
                  size="sm"
                  class="absolute right-2 top-2 h-7 w-7 p-0"
                  onclick={() => copyToClipboard(playerSdk.codeExamples[selectedFramework])}
                >
                  <CopyIcon class="w-3.5 h-3.5" />
                </Button>
              </div>
            </div>
          </div>
        </div>

        <!-- Studio SDK -->
        <div class="slab col-span-full">
          <div class="slab-header flex items-center justify-between">
            <div class="flex items-center gap-3">
              <div class="w-10 h-10 rounded-lg bg-info/10 flex items-center justify-center">
                <RadioIcon class="w-5 h-5 text-info" />
              </div>
              <div>
                <h3 class="font-semibold text-foreground">{studioSdk.name}</h3>
                <span class="text-xs text-muted-foreground">v{studioSdk.version}</span>
              </div>
            </div>
            <div class="flex items-center gap-2">
              <Button
                variant="outline"
                size="sm"
                class="gap-2 h-8"
                href={studioSdk.docsUrl}
                target="_blank"
              >
                <BookOpenIcon class="w-3.5 h-3.5" />
                Docs
              </Button>
              <Button
                variant="outline"
                size="sm"
                class="gap-2 h-8"
                href={studioSdk.githubUrl}
                target="_blank"
              >
                <GithubIcon class="w-3.5 h-3.5" />
                GitHub
              </Button>
            </div>
          </div>
          <div class="slab-body--padded space-y-6">
            <p class="text-sm text-muted-foreground">{studioSdk.description}</p>

            <!-- Features -->
            <div>
              <h4 class="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-3">Features</h4>
              <div class="grid grid-cols-2 md:grid-cols-3 gap-2">
                {#each studioSdk.features as feature}
                  <div class="flex items-center gap-2 text-sm text-foreground">
                    <CheckIcon class="w-3.5 h-3.5 text-success shrink-0" />
                    <span>{feature}</span>
                  </div>
                {/each}
              </div>
            </div>

            <!-- Install -->
            <div>
              <h4 class="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-3">Installation</h4>
              <div class="relative bg-muted/50 border border-border rounded-md p-3 font-mono text-sm">
                <code class="text-foreground">{getFrameworkInstall(studioSdk.packages, selectedFramework)}</code>
                <Button
                  variant="ghost"
                  size="sm"
                  class="absolute right-2 top-2 h-7 w-7 p-0"
                  onclick={() => copyToClipboard(getFrameworkInstall(studioSdk.packages, selectedFramework))}
                >
                  <CopyIcon class="w-3.5 h-3.5" />
                </Button>
              </div>
            </div>

            <!-- Quick Start -->
            <div>
              <h4 class="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-3">Quick Start</h4>
              <div class="relative bg-muted/50 border border-border rounded-md p-4 overflow-x-auto">
                <pre class="text-sm font-mono text-foreground whitespace-pre"><code>{studioSdk.codeExamples[selectedFramework]}</code></pre>
                <Button
                  variant="ghost"
                  size="sm"
                  class="absolute right-2 top-2 h-7 w-7 p-0"
                  onclick={() => copyToClipboard(studioSdk.codeExamples[selectedFramework])}
                >
                  <CopyIcon class="w-3.5 h-3.5" />
                </Button>
              </div>
            </div>
          </div>
        </div>

        <!-- MCP Server -->
        <div class="slab col-span-full">
          <div class="slab-header flex items-center justify-between">
            <div class="flex items-center gap-3">
              <div class="w-10 h-10 rounded-lg bg-success/10 flex items-center justify-center">
                <BotIcon class="w-5 h-5 text-success" />
              </div>
              <div>
                <h3 class="font-semibold text-foreground">{mcpServer.name}</h3>
                <span class="text-xs text-muted-foreground">Model Context Protocol</span>
              </div>
            </div>
            <div class="flex items-center gap-2">
              <Button
                variant="outline"
                size="sm"
                class="gap-2 h-8"
                href={mcpServer.docsUrl}
                target="_blank"
              >
                <BookOpenIcon class="w-3.5 h-3.5" />
                Docs
              </Button>
              <Button
                variant="outline"
                size="sm"
                class="gap-2 h-8"
                href={mcpServer.specUrl}
                target="_blank"
              >
                <ExternalLinkIcon class="w-3.5 h-3.5" />
                Spec
              </Button>
            </div>
          </div>
          <div class="slab-body--padded space-y-6">
            <p class="text-sm text-muted-foreground">{mcpServer.description}</p>

            <!-- Features -->
            <div>
              <h4 class="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-3">Capabilities</h4>
              <div class="grid grid-cols-2 md:grid-cols-3 gap-2">
                {#each mcpServer.features as feature}
                  <div class="flex items-center gap-2 text-sm text-foreground">
                    <CheckIcon class="w-3.5 h-3.5 text-success shrink-0" />
                    <span>{feature}</span>
                  </div>
                {/each}
              </div>
            </div>

            <!-- Endpoint -->
            <div>
              <h4 class="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-3">Endpoint</h4>
              <div class="relative bg-muted/50 border border-border rounded-md p-3 font-mono text-sm">
                <code class="text-foreground">{mcpServer.endpoint}</code>
                <Button
                  variant="ghost"
                  size="sm"
                  class="absolute right-2 top-2 h-7 w-7 p-0"
                  onclick={() => copyToClipboard(mcpServer.endpoint)}
                >
                  <CopyIcon class="w-3.5 h-3.5" />
                </Button>
              </div>
            </div>

            <!-- Usage note -->
            <div class="text-sm text-muted-foreground bg-muted/30 border border-border/50 rounded-md p-3">
              Configure this endpoint in your MCP-compatible AI client (Claude Desktop, Claude Code, etc).
              Requires Bearer token or wallet signature for authentication.
            </div>
          </div>
        </div>

        <!-- Upcoming SDKs -->
        <div class="slab col-span-full">
          <div class="slab-header">
            <div class="flex items-center gap-2">
              <ClockIcon class="w-4 h-4 text-warning" />
              <h3>Coming Soon</h3>
            </div>
          </div>
          <div class="slab-body--padded">
            <div class="grid grid-cols-1 md:grid-cols-3 gap-4">
              {#each upcomingSdks as sdk}
                {@const Icon = getIconByName(sdk.icon)}
                <div class="border border-border/50 rounded-lg p-4 bg-muted/20">
                  <div class="flex items-center gap-3 mb-3">
                    <div class="w-10 h-10 rounded-lg bg-muted/50 flex items-center justify-center">
                      <Icon class="w-5 h-5 text-muted-foreground" />
                    </div>
                    <div>
                      <h4 class="font-medium text-foreground">{sdk.name}</h4>
                      <span class="text-xs text-warning">{sdk.status}</span>
                    </div>
                  </div>
                  <p class="text-sm text-muted-foreground">{sdk.description}</p>
                </div>
              {/each}
            </div>
          </div>
        </div>

        <!-- Resources -->
        <div class="slab col-span-full">
          <div class="slab-header">
            <div class="flex items-center gap-2">
              <ZapIcon class="w-4 h-4 text-info" />
              <h3>Resources</h3>
            </div>
          </div>
          <div class="slab-body--padded">
            <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4">
              <a
                href={docsBaseUrl}
                target="_blank"
                rel="noopener noreferrer"
                class="flex items-center gap-3 p-4 border border-border/50 rounded-lg hover:border-primary/50 hover:bg-muted/30 transition-colors group"
              >
                <BookOpenIcon class="w-5 h-5 text-muted-foreground group-hover:text-primary" />
                <div>
                  <div class="font-medium text-foreground group-hover:text-primary">Documentation</div>
                  <div class="text-xs text-muted-foreground">Complete API reference</div>
                </div>
                <ExternalLinkIcon class="w-4 h-4 text-muted-foreground ml-auto" />
              </a>

              <a
                href={githubBaseUrl}
                target="_blank"
                rel="noopener noreferrer"
                class="flex items-center gap-3 p-4 border border-border/50 rounded-lg hover:border-primary/50 hover:bg-muted/30 transition-colors group"
              >
                <GithubIcon class="w-5 h-5 text-muted-foreground group-hover:text-primary" />
                <div>
                  <div class="font-medium text-foreground group-hover:text-primary">GitHub</div>
                  <div class="text-xs text-muted-foreground">Source code</div>
                </div>
                <ExternalLinkIcon class="w-4 h-4 text-muted-foreground ml-auto" />
              </a>

              <a
                href="https://www.npmjs.com/org/livepeer-frameworks"
                target="_blank"
                rel="noopener noreferrer"
                class="flex items-center gap-3 p-4 border border-border/50 rounded-lg hover:border-primary/50 hover:bg-muted/30 transition-colors group"
              >
                <PackageIcon class="w-5 h-5 text-muted-foreground group-hover:text-primary" />
                <div>
                  <div class="font-medium text-foreground group-hover:text-primary">npm Registry</div>
                  <div class="text-xs text-muted-foreground">All packages</div>
                </div>
                <ExternalLinkIcon class="w-4 h-4 text-muted-foreground ml-auto" />
              </a>

              <a
                href="/developer/api"
                class="flex items-center gap-3 p-4 border border-border/50 rounded-lg hover:border-primary/50 hover:bg-muted/30 transition-colors group"
              >
                <Code2Icon class="w-5 h-5 text-muted-foreground group-hover:text-primary" />
                <div>
                  <div class="font-medium text-foreground group-hover:text-primary">API Explorer</div>
                  <div class="text-xs text-muted-foreground">Test the GraphQL API</div>
                </div>
              </a>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</div>
